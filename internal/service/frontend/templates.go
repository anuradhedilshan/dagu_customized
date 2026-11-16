// Copyright (C) 2026 Yota Hamada
// SPDX-License-Identifier: GPL-3.0-or-later

package frontend

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"text/template"

	apiv1 "github.com/dagucloud/dagu/v2/api/v1"
	"github.com/dagucloud/dagu/v2/internal/cmn/config"
	"github.com/dagucloud/dagu/v2/internal/cmn/logger"
	"github.com/dagucloud/dagu/v2/internal/cmn/logger/tag"
	"github.com/dagucloud/dagu/v2/internal/license"
	"github.com/dagucloud/dagu/v2/internal/service/frontend/api/pathutil"
	workspacepkg "github.com/dagucloud/dagu/v2/internal/workspace"
)

//go:embed templates/* assets/*
var assetsFS embed.FS

const (
	templatePath     = "templates/"
	baseTemplateName = "base"
	baseTemplateFile = "base.gohtml"
)

var (
	bundleHashOnce sync.Once
	bundleHash     string
)

// formatAssetVersion returns version and hash joined by a hyphen, or
// whichever one is non-empty when the other is empty.
func formatAssetVersion(version, hash string) string {
	if version == "" {
		return hash
	}
	if hash == "" {
		return version
	}
	return version + "-" + hash
}

func hashBundle(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

func currentBundleHash() string {
	bundleHashOnce.Do(func() {
		data, err := assetsFS.ReadFile("assets/bundle.js")
		if err != nil {
			return
		}
		bundleHash = hashBundle(data)
	})
	return bundleHash
}

func currentAssetVersion() string {
	return formatAssetVersion(config.Version, currentBundleHash())
}

func (srv *Server) useTemplate(ctx context.Context, layout, name string) func(http.ResponseWriter, any) {
	if srv.config.Server.Headless {
		return func(w http.ResponseWriter, _ any) {
			http.Error(w, "Web UI is disabled in headless mode", http.StatusForbidden)
		}
	}

	files := []string{
		path.Join(templatePath, baseTemplateFile),
		path.Join(templatePath, layout),
	}
	tmpl, err := template.New(name).Funcs(defaultFunctions(&srv.funcsConfig)).ParseFS(assetsFS, files...)
	if err != nil {
		panic(err)
	}

	return func(w http.ResponseWriter, data any) {
		rendered, err := executeTemplate(tmpl, baseTemplateName, data)
		if err != nil {
			logger.Error(ctx, "Template execution failed", tag.Error(err))
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, rendered)
	}
}

func executeTemplate(tmpl *template.Template, name string, data any) (*bytes.Buffer, error) {
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		return nil, err
	}
	return &buf, nil
}

// SetupRequiredChecker determines whether initial admin setup is still needed.
// Called on every HTML page render so the value is always up-to-date.
type SetupRequiredChecker interface {
	IsSetupRequired(ctx context.Context) bool
}

// UpdateChecker provides dynamic update status for template rendering.
// Called on every HTML page render so the banner reflects the latest cached check.
type UpdateChecker interface {
	UpdateStatus() UpdateStatus
}

// UpdateStatus describes how the running server relates to the latest release.
type UpdateStatus struct {
	UpdateAvailable bool
	LatestVersion   string
	// UpgradeCommand updates this installation; empty when no single command applies.
	UpgradeCommand string
	// InstallMethod names how Dagu was installed, such as "binary" or "container".
	InstallMethod string
	// RestartRequired reports that a different binary was installed since the server started.
	RestartRequired bool
	// RestartCommand restarts the installer-managed service; empty when none is found.
	RestartCommand string
	// RestartStopsRuns reports that restarting the installer-managed service
	// also ends runs in progress.
	RestartStopsRuns bool
	// RestartCanKeepRuns reports that the service can be reconfigured so a
	// restart leaves runs in progress running.
	RestartCanKeepRuns bool
	// WebUpgradeAvailable reports that an administrator can install the
	// available update from the web UI.
	WebUpgradeAvailable bool
}

type funcsConfig struct {
	NavbarColor           string
	NavbarTitle           string
	Branding              config.BrandingConfig
	BasePath              string
	APIBasePath           string
	TZ                    string
	TzOffsetInSec         int
	MaxDashboardPageLimit int
	RemoteNodes           []string
	Permissions           map[config.Permission]bool
	Paths                 config.PathsConfig
	AuthMode              config.AuthMode
	OIDCEnabled           bool
	OIDCButtonLabel       string
	ProxyEnabled          bool
	ProxyButtonLabel      string
	TerminalEnabled       bool
	GitSyncEnabled        bool
	WorkspaceStore        workspacepkg.Store

	SetupRequiredChecker SetupRequiredChecker
	UpdateChecker        UpdateChecker
	LicenseChecker       license.Checker
	LicenseManager       *license.Manager
}

func defaultFunctions(cfg *funcsConfig) template.FuncMap {
	boolStr := func(b bool) string { return strconv.FormatBool(b) }
	licenseStatus := func() license.Status {
		if cfg.LicenseManager != nil {
			return cfg.LicenseManager.Status()
		}
		return license.StatusFor(cfg.LicenseChecker)
	}

	return template.FuncMap{
		"defTitle":     func(v any) string { s, _ := v.(string); return s },
		"version":      func() string { return config.Version },
		"assetVersion": currentAssetVersion,
		"navbarColor":  func() string { return cfg.NavbarColor },
		"navbarTitle":  func() string { return cfg.NavbarTitle },
		"brandingTitle": func() string {
			if cfg.Branding.Title != "" {
				return cfg.Branding.Title
			}
			return cfg.NavbarTitle
		},
		"brandingLogoURL": func() string {
			if cfg.Branding.LogoFile != "" {
				return ensureLeadingSlash(path.Join(strings.TrimRight(cfg.BasePath, "/"), "assets/brand-logo"))
			}
			return cfg.Branding.LogoURL
		},
		"brandingFaviconURL": func() string {
			if cfg.Branding.FaviconFile != "" {
				return ensureLeadingSlash(path.Join(strings.TrimRight(cfg.BasePath, "/"), "assets/brand-favicon"))
			}
			if cfg.Branding.FaviconURL != "" {
				return cfg.Branding.FaviconURL
			}
			return ensureLeadingSlash(path.Join(strings.TrimRight(cfg.BasePath, "/"), "assets/favicon.ico"))
		},
		"brandingCSS": func() string { return buildBrandingCSS(cfg.Branding) },
		"brandingLightBg": func() string {
			if c := sanitizeCSSValue(cfg.Branding.Colors.Background); c != "" {
				return c
			}
			return "#f9f9fa"
		},
		"brandingDarkBg": func() string {
			if c := sanitizeCSSValue(cfg.Branding.DarkColors.Background); c != "" {
				return c
			}
			return "#0e0e11"
		},
		"basePath":              func() string { return cfg.BasePath },
		"apiURL":                func() string { return pathutil.BuildMountedAPIPath(cfg.BasePath, cfg.APIBasePath) },
		"tz":                    func() string { return cfg.TZ },
		"tzOffsetInSec":         func() int { return cfg.TzOffsetInSec },
		"maxDashboardPageLimit": func() int { return cfg.MaxDashboardPageLimit },
		"remoteNodes":           func() string { return strings.Join(cfg.RemoteNodes, ",") },
		"authMode":              func() string { return string(cfg.AuthMode) },
		"oidcButtonLabel":       func() string { return cfg.OIDCButtonLabel },
		"proxyButtonLabel":      func() string { return cfg.ProxyButtonLabel },
		"initialWorkspacesJSON": func() string {
			if cfg.WorkspaceStore == nil {
				return "[]"
			}
			workspaces, err := cfg.WorkspaceStore.List(context.Background())
			if err != nil {
				return "[]"
			}
			response := make([]apiv1.WorkspaceResponse, 0, len(workspaces))
			for _, ws := range workspaces {
				if ws == nil {
					continue
				}
				item := apiv1.WorkspaceResponse{
					Id:   ws.ID,
					Name: ws.Name,
				}
				if ws.Description != "" {
					item.Description = &ws.Description
				}
				if !ws.CreatedAt.IsZero() {
					item.CreatedAt = &ws.CreatedAt
				}
				if !ws.UpdatedAt.IsZero() {
					item.UpdatedAt = &ws.UpdatedAt
				}
				response = append(response, item)
			}
			data, err := json.Marshal(response)
			if err != nil {
				return "[]"
			}
			return string(data)
		},

		// Permission functions
		"permissionsWriteDags": func() string { return boolStr(cfg.Permissions[config.PermissionWriteDAGs]) },
		"permissionsRunDags":   func() string { return boolStr(cfg.Permissions[config.PermissionRunDAGs]) },

		// Feature toggle functions
		"oidcEnabled": func() string {
			if !cfg.OIDCEnabled {
				return "false"
			}
			if cfg.LicenseChecker != nil && !cfg.LicenseChecker.IsFeatureEnabled(license.FeatureSSO) {
				return "false"
			}
			return "true"
		},
		"proxyEnabled": func() string {
			if !cfg.ProxyEnabled {
				return "false"
			}
			if cfg.LicenseChecker != nil && !cfg.LicenseChecker.IsFeatureEnabled(license.FeatureSSO) {
				return "false"
			}
			return "true"
		},
		"terminalEnabled": func() string { return boolStr(cfg.TerminalEnabled) },
		"gitSyncEnabled":  func() string { return boolStr(cfg.GitSyncEnabled) },

		"setupRequired": func() string {
			if cfg.SetupRequiredChecker == nil {
				return "false"
			}
			return boolStr(cfg.SetupRequiredChecker.IsSetupRequired(context.Background()))
		},
		"updateStatus": func() UpdateStatus {
			if cfg.UpdateChecker == nil {
				return UpdateStatus{}
			}
			return cfg.UpdateChecker.UpdateStatus()
		},

		// License functions
		"licenseValid": func() string {
			return boolStr(licenseStatus().Valid)
		},
		"licensePlan": func() string {
			return licenseStatus().Plan
		},
		"licenseExpiry": func() string {
			status := licenseStatus()
			if status.Expiry.IsZero() {
				return ""
			}
			return status.Expiry.Format("2006-01-02T15:04:05Z")
		},
		"licenseFeatures": func() string {
			features := licenseStatus().Features
			if len(features) == 0 {
				return "[]"
			}
			b, err := json.Marshal(features)
			if err != nil {
				return "[]"
			}
			return string(b)
		},
		"licenseGracePeriod": func() string {
			return boolStr(licenseStatus().GracePeriod)
		},
		"licenseGraceEndsAt": func() string {
			status := licenseStatus()
			if status.GraceEndsAt.IsZero() {
				return ""
			}
			return status.GraceEndsAt.Format("2006-01-02T15:04:05Z")
		},
		"licenseCommunity": func() string {
			return boolStr(licenseStatus().Community)
		},
		"licenseWarningCode": func() string {
			return licenseStatus().WarningCode
		},
		"licenseError": func() string {
			return licenseStatus().Failure
		},
		"licenseSource": func() string {
			source := licenseStatus().Source
			if source.IsEnv() {
				return "env"
			}
			if source == license.SourceNone {
				return ""
			}
			return "file"
		},

		// Path configuration functions
		"pathDAGsDir":            func() string { return cfg.Paths.DAGsDir },
		"pathLogDir":             func() string { return cfg.Paths.LogDir },
		"pathSuspendFlagsDir":    func() string { return cfg.Paths.SuspendFlagsDir },
		"pathAdminLogsDir":       func() string { return cfg.Paths.AdminLogsDir },
		"pathBaseConfig":         func() string { return cfg.Paths.BaseConfig },
		"pathWikiDir":            func() string { return cfg.Paths.WikiDir },
		"pathDocsDir":            func() string { return cfg.Paths.WikiDir },
		"pathDAGRunsDir":         func() string { return cfg.Paths.DAGRunsDir },
		"pathQueueDir":           func() string { return cfg.Paths.QueueDir },
		"pathProcDir":            func() string { return cfg.Paths.ProcDir },
		"pathServiceRegistryDir": func() string { return cfg.Paths.ServiceRegistryDir },
		"pathConfigFileUsed":     func() string { return cfg.Paths.ConfigFileUsed },
		"pathUsersDir":           func() string { return cfg.Paths.UsersDir },
		"pathGitSyncDir":         func() string { return path.Join(cfg.Paths.DataDir, "gitsync") },
		"pathAuditLogsDir":       func() string { return path.Join(cfg.Paths.AdminLogsDir, "audit") },
	}
}

func sanitizeCSSValue(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || strings.ContainsAny(v, "{};<>\"'\\") {
		return ""
	}
	return v
}

func appendColorVars(b *strings.Builder, c config.BrandingColors) {
	writeVar := func(name, val string) {
		if s := sanitizeCSSValue(val); s != "" {
			b.WriteString(name)
			b.WriteString(":")
			b.WriteString(s)
			b.WriteString(";")
		}
	}
	if p := sanitizeCSSValue(c.Primary); p != "" {
		writeVar("--primary", p)
		writeVar("--primary-fill", p)
		writeVar("--ring", p)
		writeVar("--sidebar-primary", p)
		writeVar("--sidebar-ring", p)
		writeVar("--status-running", p)
		writeVar("--info", p)
	}
	if ph := sanitizeCSSValue(c.PrimaryHover); ph != "" {
		writeVar("--primary-hover", ph)
		writeVar("--primary-fill-hover", ph)
	}
	writeVar("--background", c.Background)
	if fg := sanitizeCSSValue(c.Foreground); fg != "" {
		writeVar("--foreground", fg)
		writeVar("--text-primary", fg)
		writeVar("--secondary-foreground", fg)
		writeVar("--accent-foreground", fg)
		writeVar("--text-secondary", "color-mix(in srgb, "+fg+" 72%, transparent)")
		writeVar("--muted-foreground", "color-mix(in srgb, "+fg+" 60%, transparent)")
		writeVar("--text-tertiary", "color-mix(in srgb, "+fg+" 55%, transparent)")
		if sanitizeCSSValue(c.CardForeground) == "" {
			writeVar("--card-foreground", fg)
			writeVar("--popover-foreground", fg)
		}
	}
	if card := sanitizeCSSValue(c.Card); card != "" {
		writeVar("--card", card)
		writeVar("--surface", card)
		writeVar("--popover", card)
	}
	if cfg := sanitizeCSSValue(c.CardForeground); cfg != "" {
		writeVar("--card-foreground", cfg)
		writeVar("--popover-foreground", cfg)
	}
	if sb := sanitizeCSSValue(c.Sidebar); sb != "" {
		writeVar("--sidebar", sb)
		sbfg := sanitizeCSSValue(c.SidebarForeground)
		if sbfg != "" {
			writeVar("--sidebar-hover", "color-mix(in srgb, "+sbfg+" 10%, "+sb+")")
			writeVar("--sidebar-active", "color-mix(in srgb, "+sbfg+" 16%, "+sb+")")
			writeVar("--sidebar-accent", "color-mix(in srgb, "+sbfg+" 12%, "+sb+")")
			writeVar("--sidebar-border", "color-mix(in srgb, "+sbfg+" 15%, "+sb+")")
		} else {
			writeVar("--sidebar-hover", "color-mix(in srgb, var(--sidebar-foreground) 10%, "+sb+")")
			writeVar("--sidebar-active", "color-mix(in srgb, var(--sidebar-foreground) 16%, "+sb+")")
			writeVar("--sidebar-accent", "color-mix(in srgb, var(--sidebar-foreground) 12%, "+sb+")")
			writeVar("--sidebar-border", "color-mix(in srgb, var(--sidebar-foreground) 15%, "+sb+")")
		}
	}
	if sbfg := sanitizeCSSValue(c.SidebarForeground); sbfg != "" {
		writeVar("--sidebar-foreground", sbfg)
		writeVar("--sidebar-accent-foreground", sbfg)
	}
	writeVar("--sidebar-primary", c.SidebarPrimary)
	if acc := sanitizeCSSValue(c.Accent); acc != "" {
		writeVar("--accent", acc)
		writeVar("--secondary", acc)
		writeVar("--muted", acc)
		writeVar("--surface-variant", acc)
	}
	writeVar("--border", c.Border)
}

func buildBrandingCSS(b config.BrandingConfig) string {
	var lightVars, darkVars strings.Builder
	appendColorVars(&lightVars, b.Colors)
	appendColorVars(&darkVars, b.DarkColors)

	var out strings.Builder
	if lightVars.Len() > 0 {
		out.WriteString(":root{")
		out.WriteString(lightVars.String())
		out.WriteString("}")
	}
	if darkVars.Len() > 0 {
		out.WriteString(".dark{")
		out.WriteString(darkVars.String())
		out.WriteString("}")
	}
	return out.String()
}
