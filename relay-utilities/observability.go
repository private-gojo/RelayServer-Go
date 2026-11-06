// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Gojo Research

package utilities

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

type InstanceMetadata struct {
	ProjectID         string
	Location          string
	ServiceName       string
	RevisionName      string
	ConfigurationName string
	InstanceID        string
}

var Logger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
	Level:     slog.LevelInfo,
	AddSource: true,
	ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
		switch attr.Key {
		case slog.LevelKey:
			attr.Key = "severity"
			if attr.Value.String() == "WARN" {
				attr.Value = slog.StringValue("WARNING")
			}
		case slog.MessageKey:
			attr.Key = "message"
		}
		return attr
	},
}))

func ConfigureLogger(metadata InstanceMetadata) {
	attrs := make([]any, 0, 6)
	if metadata.ProjectID != "" {
		attrs = append(attrs, "project_id", metadata.ProjectID)
	}
	if metadata.Location != "" {
		attrs = append(attrs, "location", metadata.Location)
	}
	if metadata.ServiceName != "" {
		attrs = append(attrs, "service_name", metadata.ServiceName)
	}
	if metadata.RevisionName != "" {
		attrs = append(attrs, "revision_name", metadata.RevisionName)
	}
	if metadata.ConfigurationName != "" {
		attrs = append(attrs, "configuration_name", metadata.ConfigurationName)
	}
	if metadata.InstanceID != "" {
		attrs = append(attrs, "instance_id", metadata.InstanceID)
	}
	Logger = Logger.With(attrs...)
}

func LoadInstanceMetadata() InstanceMetadata {
	metadata := InstanceMetadata{
		ServiceName:       strings.TrimSpace(os.Getenv("K_SERVICE")),
		RevisionName:      strings.TrimSpace(os.Getenv("K_REVISION")),
		ConfigurationName: strings.TrimSpace(os.Getenv("K_CONFIGURATION")),
		ProjectID:         strings.TrimSpace(os.Getenv("GOOGLE_CLOUD_PROJECT")),
	}
	if metadata.ProjectID == "" {
		metadata.ProjectID = strings.TrimSpace(os.Getenv("GCLOUD_PROJECT"))
	}

	if metadata.ServiceName != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		type result struct {
			key   string
			value string
		}
		paths := map[string]string{
			"project_id":  "/computeMetadata/v1/project/project-id",
			"region":      "/computeMetadata/v1/instance/region",
			"instance_id": "/computeMetadata/v1/instance/id",
		}
		results := make(chan result, len(paths))
		client := &http.Client{Timeout: 2 * time.Second}

		for key, path := range paths {
			go func(key, path string) {
				results <- result{key: key, value: readMetadataValue(ctx, client, path)}
			}(key, path)
		}

		for range paths {
			item := <-results
			switch item.key {
			case "project_id":
				if item.value != "" {
					metadata.ProjectID = item.value
				}
			case "region":
				if index := strings.LastIndex(item.value, "/"); index >= 0 {
					item.value = item.value[index+1:]
				}
				metadata.Location = item.value
			case "instance_id":
				metadata.InstanceID = item.value
			}
		}
	}

	if metadata.InstanceID == "" {
		metadata.InstanceID, _ = os.Hostname()
	}
	return metadata
}

func readMetadataValue(ctx context.Context, client *http.Client, path string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://metadata.google.internal"+path, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Metadata-Flavor", "Google")

	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}

	value, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(value))
}
