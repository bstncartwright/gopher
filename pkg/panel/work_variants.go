package panel

import (
	"io/fs"
	"net/http"
	"net/url"
	"strings"
)

type workVariantDescriptor struct {
	Key     string
	Label   string
	Name    string
	Tagline string
	Asset   string
}

type workVariantLink struct {
	Key    string
	Label  string
	Href   string
	Active bool
}

type workVariantPageData struct {
	PanelRoot      string
	Variant        string
	VariantLabel   string
	VariantName    string
	VariantTagline string
	Variants       []workVariantLink
	StatusStrip    []shellMetric
	CanonicalHref  string
	Work           workPageData
}

var workVariantCatalog = []workVariantDescriptor{
	{Key: "v1", Label: "V1", Name: "Brutal Command Deck", Tagline: "Industrial, severe, and queue-first.", Asset: "panel_v1.css"},
	{Key: "v2", Label: "V2", Name: "Editorial Ops Desk", Tagline: "Magazine hierarchy for operators, not marketers.", Asset: "panel_v2.css"},
	{Key: "v3", Label: "V3", Name: "Radar Tactical Grid", Tagline: "Tracking board meets live mission console.", Asset: "panel_v3.css"},
	{Key: "v4", Label: "V4", Name: "Terminal Newsroom", Tagline: "Dense signal, restrained color, fast scanning.", Asset: "panel_v4.css"},
	{Key: "v5", Label: "V5", Name: "Analog Avionics", Tagline: "Amber instrumentation with cockpit density.", Asset: "panel_v5.css"},
	{Key: "v6", Label: "V6", Name: "Printroom Board", Tagline: "Light-mode operations wall with hard paper rhythm.", Asset: "panel_v6.css"},
	{Key: "v7", Label: "V7", Name: "Museum Minimal", Tagline: "Quiet luxury for severe operational review.", Asset: "panel_v7.css"},
	{Key: "v8", Label: "V8", Name: "Signal Board", Tagline: "Status-first emergency response shell.", Asset: "panel_v8.css"},
}

func (s *Server) handleWorkVariantEntry(w http.ResponseWriter, r *http.Request) {
	s.handleWorkVariantPage(w, r)
}

func (s *Server) handleWorkVariantPage(w http.ResponseWriter, r *http.Request) {
	variant := normalizeWorkVariant(r.PathValue("variant"))
	if variant == "" {
		http.NotFound(w, r)
		return
	}
	descriptor, ok := workVariantDescriptorByKey(variant)
	if !ok {
		http.NotFound(w, r)
		return
	}

	panelRoot := "/" + variant + adminRoot
	sessionID := strings.TrimSpace(r.PathValue("sessionID"))
	data := workVariantPageData{
		PanelRoot:      panelRoot,
		Variant:        variant,
		VariantLabel:   descriptor.Label,
		VariantName:    descriptor.Name,
		VariantTagline: descriptor.Tagline,
		Variants:       buildWorkVariantLinks(variant, sessionID, r.URL.RawQuery),
		CanonicalHref:  adminRoot + "/work",
		StatusStrip: func() []shellMetric {
			shell := s.buildShellMetrics(r.Context())
			return []shellMetric{
				{Label: "Active", Value: shell.Active, Tone: "good"},
				{Label: "Failed", Value: shell.Failed, Tone: "danger"},
				{Label: "Waiting", Value: shell.Waiting, Tone: "warn"},
				{Label: "Delegated", Value: shell.Delegated, Tone: "neutral"},
				{Label: "Automations", Value: shell.AutomationsEnabled, Tone: "good"},
				{Label: "Paused", Value: shell.AutomationsPaused, Tone: "muted"},
				{Label: "Nodes", Value: shell.NodeCount, Tone: "neutral"},
			}
		}(),
		Work: workPageData{
			HasSessionStore: s.store != nil,
			InitialSession:  sessionID,
			InitialFilter:   normalizeWorkFilter(r.URL.Query().Get("filter")),
			InitialView:     normalizeWorkView(r.URL.Query().Get("view")),
			InitialNoise:    normalizeWorkNoise(r.URL.Query().Get("noise")),
			InitialEventSeq: strings.TrimSpace(r.URL.Query().Get("event")),
		},
	}
	s.renderTemplate(w, "work_variant_page.html", data)
}

func (s *Server) handleWorkVariantBaseCSS(w http.ResponseWriter, r *http.Request) {
	if normalizeWorkVariant(r.PathValue("variant")) == "" {
		http.NotFound(w, r)
		return
	}
	blob, err := fs.ReadFile(s.assets, "panel_variants.css")
	if err != nil {
		http.Error(w, "asset not found", http.StatusNotFound)
		return
	}
	w.Header().Set("content-type", "text/css; charset=utf-8")
	_, _ = w.Write(blob)
}

func (s *Server) handleWorkVariantCSS(w http.ResponseWriter, r *http.Request) {
	variant := normalizeWorkVariant(r.PathValue("variant"))
	if variant == "" {
		http.NotFound(w, r)
		return
	}
	descriptor, ok := workVariantDescriptorByKey(variant)
	if !ok {
		http.NotFound(w, r)
		return
	}
	blob, err := fs.ReadFile(s.assets, descriptor.Asset)
	if err != nil {
		http.Error(w, "asset not found", http.StatusNotFound)
		return
	}
	w.Header().Set("content-type", "text/css; charset=utf-8")
	_, _ = w.Write(blob)
}

func normalizeWorkVariant(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "v1", "v2", "v3", "v4", "v5", "v6", "v7", "v8":
		return value
	default:
		return ""
	}
}

func workVariantDescriptorByKey(key string) (workVariantDescriptor, bool) {
	key = normalizeWorkVariant(key)
	for _, descriptor := range workVariantCatalog {
		if descriptor.Key == key {
			return descriptor, true
		}
	}
	return workVariantDescriptor{}, false
}

func buildWorkVariantLinks(activeVariant, sessionID, rawQuery string) []workVariantLink {
	links := make([]workVariantLink, 0, len(workVariantCatalog))
	suffix := "/work"
	if sessionID = strings.TrimSpace(sessionID); sessionID != "" {
		suffix += "/" + url.PathEscape(sessionID)
	}
	if query := strings.TrimSpace(rawQuery); query != "" {
		suffix += "?" + query
	}
	for _, descriptor := range workVariantCatalog {
		panelRoot := "/" + descriptor.Key + adminRoot
		links = append(links, workVariantLink{
			Key:    descriptor.Key,
			Label:  descriptor.Label,
			Href:   panelRoot + suffix,
			Active: descriptor.Key == activeVariant,
		})
	}
	return links
}
