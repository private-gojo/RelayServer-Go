//! The badvpn `udpgw` protocol (github.com/ambrop72/badvpn, `udpgw/` + `protocol/`) — wire format
//! and server logic.

use std::collections::HashMap;
use std::io::{self, ErrorKind};
use std::net::{Ipv4Addr, Ipv6Addr, SocketAddr};
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex, OnceLock};
use std::time::{Duration, Instant};

use log::{debug, error, info, warn};
use tokio::io::{AsyncRead, AsyncReadExt, AsyncWrite, AsyncWriteExt};
use tokio::net::UdpSocket;
use tokio::sync::{OwnedSemaphorePermit, Semaphore, mpsc};
use tokio::task::JoinHandle;

pub const FLAG_KEEPALIVE: u8 = 0x01;
pub const FLAG_REBIND: u8 = 0x02;
pub const FLAG_DNS: u8 = 0x04;
pub const FLAG_IPV6: u8 = 0x08;

pub const HDR_LEN: usize = 3;
pub const ADDR_V4_LEN: usize = 6;
pub const ADDR_V6_LEN: usize = 18;

pub const DEFAULT_UDP_MTU: usize = 65_520;
const MAX_DL_PAYLOAD: usize = u16::MAX as usize - HDR_LEN - ADDR_V6_LEN;
const CLIENT_DISCONNECT: Duration = Duration::from_secs(20);
pub const DEFAULT_IDLE_TIMEOUT: Duration = Duration::from_secs(120);
const DL_CHAN_CAP: usize = 256;
const UPLINK_BUF: usize = 2 + u16::MAX as usize;
const DNS_REFRESH: Duration = Duration::from_secs(2);
const WRITER_GRACE: Duration = Duration::from_secs(1);

static ACTIVE_UDP_CONNECTIONS: AtomicU64 = AtomicU64::new(0);
static UDP_UP_PACKETS: AtomicU64 = AtomicU64::new(0);
static UDP_UP_BYTES: AtomicU64 = AtomicU64::new(0);
static UDP_DOWN_PACKETS: AtomicU64 = AtomicU64::new(0);
static UDP_DOWN_BYTES: AtomicU64 = AtomicU64::new(0);
static UDP_SOCKET_ERRORS: AtomicU64 = AtomicU64::new(0);
static UDP_SEND_ERRORS: AtomicU64 = AtomicU64::new(0);
static UDP_IDLE_CLOSED: AtomicU64 = AtomicU64::new(0);
static UDP_LRU_EVICTIONS: AtomicU64 = AtomicU64::new(0);

pub fn stats_line(active_clients: usize) -> String {
    format!(
        "active_clients={} active_udp_connections={} udp_up_packets={} udp_up_bytes={} \
         udp_down_packets={} udp_down_bytes={} socket_errors={} send_errors={} \
         idle_closed={} lru_evictions={}",
        active_clients,
        ACTIVE_UDP_CONNECTIONS.load(Ordering::Relaxed),
        UDP_UP_PACKETS.load(Ordering::Relaxed),
        UDP_UP_BYTES.load(Ordering::Relaxed),
        UDP_DOWN_PACKETS.load(Ordering::Relaxed),
        UDP_DOWN_BYTES.load(Ordering::Relaxed),
        UDP_SOCKET_ERRORS.load(Ordering::Relaxed),
        UDP_SEND_ERRORS.load(Ordering::Relaxed),
        UDP_IDLE_CLOSED.load(Ordering::Relaxed),
        UDP_LRU_EVICTIONS.load(Ordering::Relaxed),
    )
}

#[derive(Debug, Clone)]
pub struct Config {
    pub max_connections: usize,
    pub udp_mtu: usize,
    pub dns_server: Option<SocketAddr>,
    pub socket_permits: Arc<Semaphore>,
    pub idle_timeout: Duration,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct UdpgwMsg<'a> {
    pub flags: u8,
    pub conid: u16,
    pub dest: Option<SocketAddr>,
    pub payload: &'a [u8],
}

impl UdpgwMsg<'_> {
    pub fn is_keepalive(&self) -> bool {
        self.flags & FLAG_KEEPALIVE != 0
    }
    pub fn is_rebind(&self) -> bool {
        self.flags & FLAG_REBIND != 0
    }
    pub fn is_dns(&self) -> bool {
        self.flags & FLAG_DNS != 0
    }
}

pub fn parse_msg(block: &[u8]) -> io::Result<UdpgwMsg<'_>> {
    if block.len() < HDR_LEN {
        return Err(io::Error::new(ErrorKind::InvalidData, "missing header"));
    }
    let flags = block[0];
    let conid = u16::from_le_bytes([block[1], block[2]]);
    let rest = &block[HDR_LEN..];

    if flags & FLAG_KEEPALIVE != 0 {
        return Ok(UdpgwMsg {
            flags,
            conid,
            dest: None,
            payload: &[],
        });
    }

    let (dest, addr_len) = if flags & FLAG_IPV6 != 0 {
        if rest.len() < ADDR_V6_LEN {
            return Err(io::Error::new(
                ErrorKind::InvalidData,
                "missing ipv6 address",
            ));
        }
        let octets = <[u8; 16]>::try_from(&rest[0..16]).unwrap();
        let ip = Ipv6Addr::from(octets);
        let port = u16::from_be_bytes([rest[16], rest[17]]);
        (SocketAddr::new(ip.into(), port), ADDR_V6_LEN)
    } else {
        if rest.len() < ADDR_V4_LEN {
            return Err(io::Error::new(
                ErrorKind::InvalidData,
                "missing ipv4 address",
            ));
        }
        let ip = Ipv4Addr::new(rest[0], rest[1], rest[2], rest[3]);
        let port = u16::from_be_bytes([rest[4], rest[5]]);
        (SocketAddr::new(ip.into(), port), ADDR_V4_LEN)
    };

    Ok(UdpgwMsg {
        flags,
        conid,
        dest: Some(dest),
        payload: &rest[addr_len..],
    })
}

pub fn build_downlink_frame(conid: u16, report: SocketAddr, payload: &[u8]) -> Vec<u8> {
    let (flags, addr_len) = match report {
        SocketAddr::V4(_) => (0u8, ADDR_V4_LEN),
        SocketAddr::V6(_) => (FLAG_IPV6, ADDR_V6_LEN),
    };
    let body_len = HDR_LEN + addr_len + payload.len();
    debug_assert!(
        body_len <= u16::MAX as usize,
        "udpgw frame exceeds the u16 packetproto prefix"
    );

    let mut out = Vec::with_capacity(2 + body_len);
    out.extend_from_slice(&(body_len as u16).to_le_bytes());
    out.push(flags);
    out.extend_from_slice(&conid.to_le_bytes());
    match report {
        SocketAddr::V4(a) => {
            out.extend_from_slice(&a.ip().octets());
            out.extend_from_slice(&a.port().to_be_bytes());
        }
        SocketAddr::V6(a) => {
            out.extend_from_slice(&a.ip().octets());
            out.extend_from_slice(&a.port().to_be_bytes());
        }
    }
    out.extend_from_slice(payload);
    out
}

pub struct FrameReader<R> {
    rd: R,
}

impl<R: AsyncRead + Unpin> FrameReader<R> {
    pub fn new(rd: R) -> Self {
        FrameReader { rd }
    }

    pub async fn next_msg(&mut self) -> io::Result<Option<Vec<u8>>> {
        let mut lenb = [0u8; 2];
        if self.rd.read(&mut lenb[..1]).await? == 0 {
            return Ok(None);
        }
        self.rd.read_exact(&mut lenb[1..]).await?;
        let len = u16::from_le_bytes(lenb) as usize;
        let mut block = vec![0u8; len];
        self.rd.read_exact(&mut block).await?;
        Ok(Some(block))
    }
}

struct UdpConn {
    sock: Arc<UdpSocket>,
    orig_dest: SocketAddr,
    send_dest: SocketAddr,
    last_use: Arc<AtomicU64>,
    last_activity_ms: Arc<AtomicU64>,
    gen_id: u64,
    down: JoinHandle<()>,
    _permit: OwnedSemaphorePermit,
}

impl Drop for UdpConn {
    fn drop(&mut self) {
        ACTIVE_UDP_CONNECTIONS.fetch_sub(1, Ordering::Relaxed);
        self.down.abort();
    }
}

#[inline]
fn bump(last_use: &AtomicU64, tick: &AtomicU64) {
    last_use.store(tick.fetch_add(1, Ordering::Relaxed), Ordering::Relaxed);
}

#[inline]
fn ms_since(epoch: tokio::time::Instant) -> u64 {
    epoch.elapsed().as_millis() as u64
}

#[inline]
fn stamp_activity(last_activity_ms: &AtomicU64, epoch: tokio::time::Instant) {
    last_activity_ms.fetch_max(ms_since(epoch), Ordering::Relaxed);
}

fn is_idle(last_ms: u64, now_ms: u64, idle_ms: u64) -> bool {
    idle_ms > 0 && now_ms.saturating_sub(last_ms) >= idle_ms
}

fn sweep_interval(idle: Duration) -> Duration {
    std::cmp::min(Duration::from_secs(1), idle / 4)
}

fn should_sweep(idle_ms: u64, since_last: Duration, interval: Duration) -> bool {
    idle_ms > 0 && since_last >= interval
}

fn sweep_idle(conns: &mut HashMap<u16, UdpConn>, now_ms: u64, idle_ms: u64) -> usize {
    let before = conns.len();
    conns.retain(|_, c| !is_idle(c.last_activity_ms.load(Ordering::Relaxed), now_ms, idle_ms));
    let closed = before - conns.len();
    UDP_IDLE_CLOSED.fetch_add(closed as u64, Ordering::Relaxed);
    closed
}

pub async fn serve<S>(stream: S, client: String, cfg: Arc<Config>)
where
    S: AsyncRead + AsyncWrite + Unpin + Send + 'static,
{
    let client: Arc<str> = client.into();
    let epoch = tokio::time::Instant::now();
    let idle_ms = u64::try_from(cfg.idle_timeout.as_millis()).unwrap_or(u64::MAX);
    let interval = sweep_interval(cfg.idle_timeout);
    let mut last_sweep = epoch;

    let (rd, wr) = tokio::io::split(stream);
    let (dl_tx, dl_rx) = mpsc::channel::<Vec<u8>>(DL_CHAN_CAP);
    let (cleanup_tx, mut cleanup_rx) = mpsc::channel::<(u16, u64)>(64);
    let mut writer = tokio::spawn(writer_task(wr, dl_rx));

    let rd = tokio::io::BufReader::with_capacity(UPLINK_BUF, rd);
    let mut fr = FrameReader::new(rd);
    let mut conns: HashMap<u16, UdpConn> = HashMap::new();
    let tick = Arc::new(AtomicU64::new(0));

    loop {
        while let Ok((conid, gen_id)) = cleanup_rx.try_recv() {
            if conns.get(&conid).map(|c| c.gen_id) == Some(gen_id) {
                conns.remove(&conid);
            }
        }
        let block = match tokio::time::timeout(CLIENT_DISCONNECT, fr.next_msg()).await {
            Ok(Ok(Some(b))) => b,
            Ok(Ok(None)) => {
                info!("client ({client}): client closed");
                break;
            }
            Ok(Err(e)) => {
                info!("client ({client}): client error: {e}");
                break;
            }
            Err(_) => {
                info!("client ({client}): timed out, disconnecting");
                break;
            }
        };
        if should_sweep(idle_ms, last_sweep.elapsed(), interval) {
            let closed = sweep_idle(&mut conns, ms_since(epoch), idle_ms);
            if closed > 0 {
                debug!("client ({client}): closed {closed} idle connection(s)");
            }
            last_sweep = tokio::time::Instant::now();
        }
        handle_block(
            &mut conns,
            &dl_tx,
            &cleanup_tx,
            &tick,
            epoch,
            &cfg,
            &client,
            &block,
        )
        .await;
    }

    conns.clear();
    drop(dl_tx);
    if tokio::time::timeout(WRITER_GRACE, &mut writer)
        .await
        .is_err()
    {
        writer.abort();
    }
}

#[allow(clippy::too_many_arguments)]
async fn handle_block(
    conns: &mut HashMap<u16, UdpConn>,
    dl_tx: &mpsc::Sender<Vec<u8>>,
    cleanup_tx: &mpsc::Sender<(u16, u64)>,
    tick: &Arc<AtomicU64>,
    epoch: tokio::time::Instant,
    cfg: &Arc<Config>,
    client: &Arc<str>,
    block: &[u8],
) {
    let msg = match parse_msg(block) {
        Ok(m) => m,
        Err(e) => {
            error!("client ({client}): {e}");
            return;
        }
    };
    if msg.is_keepalive() {
        debug!("client ({client}): received keepalive");
        return;
    }
    let Some(orig_dest) = msg.dest else { return };

    if msg.payload.len() > cfg.udp_mtu {
        error!("client ({client}): too much data");
        return;
    }

    let send_dest = if msg.is_dns() {
        match current_resolver(cfg.dns_server) {
            Some(r) => r,
            None => {
                warn!("client ({client}): received DNS packet, but no DNS server available");
                orig_dest
            }
        }
    } else {
        orig_dest
    };

    let reopen = match conns.get(&msg.conid) {
        None => true,
        Some(c) => msg.is_rebind() || c.orig_dest != orig_dest,
    };

    if reopen {
        if conns.remove(&msg.conid).is_some() {
            debug!(
                "client ({client}): connection {} ({orig_dest}): close old",
                msg.conid
            );
        }

        if conns.len() >= cfg.max_connections {
            let victim = conns
                .iter()
                .min_by_key(|(_, c)| c.last_use.load(Ordering::Relaxed))
                .map(|(k, _)| *k);
            if let Some(v) = victim {
                debug!("client ({client}): connection {v}: closing least recently used");
                if conns.remove(&v).is_some() {
                    UDP_LRU_EVICTIONS.fetch_add(1, Ordering::Relaxed);
                }
            }
        }

        let Ok(permit) = cfg.socket_permits.clone().try_acquire_owned() else {
            debug!(
                "client ({client}): connection {}: total connection limit reached, dropping",
                msg.conid
            );
            return;
        };
        match open_conn(
            msg.conid,
            orig_dest,
            send_dest,
            permit,
            dl_tx.clone(),
            cleanup_tx.clone(),
            tick.clone(),
            epoch,
            cfg.udp_mtu,
            client.clone(),
        )
        .await
        {
            Some(c) => {
                conns.insert(msg.conid, c);
            }
            None => return,
        }
    }

    if let Some(c) = conns.get(&msg.conid) {
        bump(&c.last_use, tick);
        stamp_activity(&c.last_activity_ms, epoch);
        if !msg.payload.is_empty() {
            match c.sock.send_to(msg.payload, c.send_dest).await {
                Ok(sent) => {
                    UDP_UP_PACKETS.fetch_add(1, Ordering::Relaxed);
                    UDP_UP_BYTES.fetch_add(sent as u64, Ordering::Relaxed);
                }
                Err(e) => {
                    UDP_SEND_ERRORS.fetch_add(1, Ordering::Relaxed);
                    warn!("client ({client}): connection {} ({}): UDP send failed: {e}", msg.conid, c.send_dest);
                }
            }
        }
    }
}

#[allow(clippy::too_many_arguments)]
async fn open_conn(
    conid: u16,
    orig_dest: SocketAddr,
    send_dest: SocketAddr,
    permit: OwnedSemaphorePermit,
    dl_tx: mpsc::Sender<Vec<u8>>,
    cleanup_tx: mpsc::Sender<(u16, u64)>,
    tick: Arc<AtomicU64>,
    epoch: tokio::time::Instant,
    udp_mtu: usize,
    client: Arc<str>,
) -> Option<UdpConn> {
    let bind: SocketAddr = if send_dest.is_ipv4() {
        SocketAddr::from(([0u8, 0, 0, 0], 0))
    } else {
        SocketAddr::from(([0u16; 8], 0))
    };
    let sock = match UdpSocket::bind(bind).await {
        Ok(s) => Arc::new(s),
        Err(e) => {
            UDP_SOCKET_ERRORS.fetch_add(1, Ordering::Relaxed);
            warn!("client ({client}): connection {conid} ({orig_dest}): UDP socket failed: {e}");
            return None;
        }
    };
    let gen_id = tick.fetch_add(1, Ordering::Relaxed);
    let last_use = Arc::new(AtomicU64::new(gen_id));
    let last_activity_ms = Arc::new(AtomicU64::new(ms_since(epoch)));

    ACTIVE_UDP_CONNECTIONS.fetch_add(1, Ordering::Relaxed);
    let down = tokio::spawn(downlink_task(
        sock.clone(),
        conid,
        gen_id,
        orig_dest,
        dl_tx,
        cleanup_tx,
        last_use.clone(),
        tick,
        last_activity_ms.clone(),
        epoch,
        udp_mtu,
        client,
    ));
    Some(UdpConn {
        sock,
        orig_dest,
        send_dest,
        last_use,
        last_activity_ms,
        gen_id,
        down,
        _permit: permit,
    })
}

#[allow(clippy::too_many_arguments)]
async fn downlink_task(
    sock: Arc<UdpSocket>,
    conid: u16,
    gen_id: u64,
    reply_addr: SocketAddr,
    dl_tx: mpsc::Sender<Vec<u8>>,
    cleanup_tx: mpsc::Sender<(u16, u64)>,
    last_use: Arc<AtomicU64>,
    tick: Arc<AtomicU64>,
    last_activity_ms: Arc<AtomicU64>,
    epoch: tokio::time::Instant,
    udp_mtu: usize,
    client: Arc<str>,
) {
    let mut buf = vec![0u8; udp_mtu];
    loop {
        let n = match sock.recv_from(&mut buf).await {
            Ok((n, _src)) => n,
            Err(e) => {
                UDP_SOCKET_ERRORS.fetch_add(1, Ordering::Relaxed);
                info!("client ({client}): connection {conid} ({reply_addr}): UDP error: {e}");
                let _ = cleanup_tx.try_send((conid, gen_id));
                break;
            }
        };
        stamp_activity(&last_activity_ms, epoch);
        bump(&last_use, &tick);
        if n > MAX_DL_PAYLOAD {
            warn!(
                "client ({client}): connection {conid} ({reply_addr}): packet is too large, cannot send to client"
            );
            continue;
        }
        let frame = build_downlink_frame(conid, reply_addr, &buf[..n]);
        if dl_tx.send(frame).await.is_err() {
            break;
        }
        UDP_DOWN_PACKETS.fetch_add(1, Ordering::Relaxed);
        UDP_DOWN_BYTES.fetch_add(n as u64, Ordering::Relaxed);
    }
}

async fn writer_task<W: AsyncWrite + Unpin>(mut wr: W, mut dl_rx: mpsc::Receiver<Vec<u8>>) {
    while let Some(frame) = dl_rx.recv().await {
        if wr.write_all(&frame).await.is_err() {
            break;
        }
    }
    let _ = wr.shutdown().await;
}

static RESOLVER_CACHE: OnceLock<Mutex<Option<(SocketAddr, Instant)>>> = OnceLock::new();

fn current_resolver(override_addr: Option<SocketAddr>) -> Option<SocketAddr> {
    if override_addr.is_some() {
        return override_addr;
    }
    let cell = RESOLVER_CACHE.get_or_init(|| Mutex::new(None));
    let mut g = cell.lock().unwrap_or_else(|e| e.into_inner());
    if let Some((addr, at)) = *g
        && at.elapsed() < DNS_REFRESH
    {
        return Some(addr);
    }
    match parse_resolv_conf() {
        Some(a) => {
            if g.as_ref().map(|(old, _)| *old) != Some(a) {
                info!("using DNS server {a}");
            }
            *g = Some((a, Instant::now()));
            Some(a)
        }
        None => g.as_ref().map(|(a, _)| *a),
    }
}

pub fn dns_status() -> String {
    match current_resolver(None) {
        Some(addr) => format!("resolver={addr}"),
        None => "resolver=unavailable; DNS-flagged packets will use their requested destination".to_string(),
    }
}

fn parse_resolv_conf() -> Option<SocketAddr> {
    let text = std::fs::read_to_string("/etc/resolv.conf").ok()?;
    parse_resolv_conf_text(&text)
}

fn parse_resolv_conf_text(text: &str) -> Option<SocketAddr> {
    for line in text.lines() {
        let line = line.split(['#', ';']).next()?.trim();
        let mut fields = line.split_whitespace();
        if fields.next() != Some("nameserver") {
            continue;
        }
        if let Some(v4) = fields.next().and_then(|value| value.parse::<Ipv4Addr>().ok()) {
            return Some(SocketAddr::from((v4, 53)));
        }
    }
    None
}
