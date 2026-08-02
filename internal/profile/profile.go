package profile

import (
	"errors"
	"fmt"
	"strings"

	"github.com/kaiizer777/onyx-scrapper/internal/store"
)

// newsKeywords is a set of lower-cased terms that strongly indicate a news/trend query.
var newsKeywords = []string{
	"news", "latest", "recent", "trending", "trend", "today",
	"breaking", "headline", "headlines", "update", "updates",
	"current", "happening", "now", "this week", "this month",
	"2024", "2025", "2026", "new releases", "announcements",
}

// IsNewsQuery returns true when the query appears to be asking for news or current trends.
// It is intentionally broad — false positives are cheaper than missed injections.
func IsNewsQuery(query string) bool {
	lower := strings.ToLower(query)
	for _, kw := range newsKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// BuildNewsContext returns a formatted instruction block listing the user's enabled profile
// interest fields. The caller should inject this into the LLM system prompt only when the
// query is news-related. Returns ("", false) when no enabled fields are configured.
func BuildNewsContext(fields []store.ProfileField) (string, bool) {
	var enabled []store.ProfileField
	for _, f := range fields {
		if f.Enabled {
			enabled = append(enabled, f)
		}
	}
	if len(enabled) == 0 {
		return "", false
	}

	var sb strings.Builder
	sb.WriteString("USER INTEREST PROFILE — NEWS CONTEXT:\n")
	sb.WriteString("The user has configured the following interest topics. When fetching news or trends, ")
	sb.WriteString("ONLY cover these topics (in priority order). For each topic search for the LATEST and MOST RECENT news first.\n\n")

	for i, f := range enabled {
		keywords := strings.Split(f.KeywordsCSV, ",")
		var cleanKW []string
		for _, k := range keywords {
			if t := strings.TrimSpace(k); t != "" {
				cleanKW = append(cleanKW, t)
			}
		}
		sb.WriteString(fmt.Sprintf("  %d. Topic: %q | Keywords: [%s]\n",
			i+1, f.FieldName, strings.Join(cleanKW, ", ")))
	}

	sb.WriteString("\nSEARCH INSTRUCTION: For each topic above, append 'latest news' or the current year to your search queries. ")
	sb.WriteString("Prioritize articles from the last 7 days. Do NOT fetch news for topics not listed above.")

	return sb.String(), true
}

const (
	DefaultProfileName = "Default Profile"
	DefaultMaxFields   = 10
)

var (
	ErrEmptyFieldName     = errors.New("field name cannot be empty")
	ErrEmptyKeywords      = errors.New("keywords CSV must contain at least one valid keyword")
	ErrDuplicateFieldName = errors.New("a field with this name already exists in the profile")
	ErrMaxFieldsExceeded  = errors.New("maximum allowed profile fields limit exceeded")
	ErrProfileNotFound    = errors.New("profile not found")
	ErrFieldNotFound      = errors.New("profile field not found")
)

type Config struct {
	MaxFields int
}

type Manager struct {
	store *store.Store
	cfg   Config
}

func NewManager(st *store.Store, cfg Config) *Manager {
	if cfg.MaxFields <= 0 {
		cfg.MaxFields = DefaultMaxFields
	}
	return &Manager{
		store: st,
		cfg:   cfg,
	}
}

type ProfileWithFields struct {
	Profile *store.UserProfile   `json:"profile"`
	Fields  []store.ProfileField `json:"fields"`
}

// GetOrCreateDefaultProfile returns the default user profile, creating it if absent.
func (m *Manager) GetOrCreateDefaultProfile() (*store.UserProfile, error) {
	p, err := m.store.GetProfileByName(DefaultProfileName)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch default profile: %w", err)
	}
	if p != nil {
		return p, nil
	}

	p, err = m.store.CreateProfile(DefaultProfileName)
	if err != nil {
		p2, err2 := m.store.GetProfileByName(DefaultProfileName)
		if err2 == nil && p2 != nil {
			return p2, nil
		}
		return nil, fmt.Errorf("failed to auto-create default profile: %w", err)
	}
	return p, nil
}

// ValidateFieldName ensures field_name is non-empty after trim.
func ValidateFieldName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", ErrEmptyFieldName
	}
	return trimmed, nil
}

// ValidateKeywordsCSV trims and splits keywords by comma, verifying at least 1 keyword remains.
func ValidateKeywordsCSV(keywordsCSV string) (string, []string, error) {
	parts := strings.Split(keywordsCSV, ",")
	var cleaned []string
	for _, p := range parts {
		t := strings.TrimSpace(p)
		if t != "" {
			cleaned = append(cleaned, t)
		}
	}
	if len(cleaned) == 0 {
		return "", nil, ErrEmptyKeywords
	}
	return strings.Join(cleaned, ", "), cleaned, nil
}

// AddField validates and adds a new field to a profile.
func (m *Manager) AddField(profileID int64, fieldName, keywordsCSV string, priorityOrder int, enabled bool) (*store.ProfileField, error) {
	name, err := ValidateFieldName(fieldName)
	if err != nil {
		return nil, err
	}

	cleanCSV, _, err := ValidateKeywordsCSV(keywordsCSV)
	if err != nil {
		return nil, err
	}

	p, err := m.store.GetProfile(profileID)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, ErrProfileNotFound
	}

	count, err := m.store.CountProfileFields(profileID)
	if err != nil {
		return nil, err
	}
	if count >= m.cfg.MaxFields {
		return nil, fmt.Errorf("%w (%d max)", ErrMaxFieldsExceeded, m.cfg.MaxFields)
	}

	existingFields, err := m.store.ListProfileFields(profileID)
	if err != nil {
		return nil, err
	}
	for _, ef := range existingFields {
		if strings.EqualFold(ef.FieldName, name) {
			return nil, ErrDuplicateFieldName
		}
	}

	f, err := m.store.CreateProfileField(store.ProfileField{
		ProfileID:     profileID,
		FieldName:     name,
		KeywordsCSV:   cleanCSV,
		PriorityOrder: priorityOrder,
		Enabled:       enabled,
	})
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return nil, ErrDuplicateFieldName
		}
		return nil, err
	}

	return f, nil
}

// UpdateField validates and updates an existing profile field.
func (m *Manager) UpdateField(field store.ProfileField) error {
	name, err := ValidateFieldName(field.FieldName)
	if err != nil {
		return err
	}

	cleanCSV, _, err := ValidateKeywordsCSV(field.KeywordsCSV)
	if err != nil {
		return err
	}

	field.FieldName = name
	field.KeywordsCSV = cleanCSV

	existingFields, err := m.store.ListProfileFields(field.ProfileID)
	if err != nil {
		return err
	}
	for _, ef := range existingFields {
		if ef.ID != field.ID && strings.EqualFold(ef.FieldName, name) {
			return ErrDuplicateFieldName
		}
	}

	if err := m.store.UpdateProfileField(field); err != nil {
		if strings.Contains(err.Error(), "not found") {
			return ErrFieldNotFound
		}
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return ErrDuplicateFieldName
		}
		return err
	}
	return nil
}

// RemoveField deletes a profile field.
func (m *Manager) RemoveField(fieldID int64) error {
	err := m.store.DeleteProfileField(fieldID)
	if err != nil && strings.Contains(err.Error(), "not found") {
		return ErrFieldNotFound
	}
	return err
}

// GetProfileWithFields loads a profile along with all its fields.
func (m *Manager) GetProfileWithFields(profileID int64) (*ProfileWithFields, error) {
	p, err := m.store.GetProfile(profileID)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, ErrProfileNotFound
	}

	fields, err := m.store.ListProfileFields(profileID)
	if err != nil {
		return nil, err
	}

	return &ProfileWithFields{
		Profile: p,
		Fields:  fields,
	}, nil
}

// SyncFields validates and atomically updates the list of fields for a profile.
func (m *Manager) SyncFields(profileID int64, fields []store.ProfileField) ([]store.ProfileField, error) {
	if len(fields) > m.cfg.MaxFields {
		return nil, fmt.Errorf("%w (%d max)", ErrMaxFieldsExceeded, m.cfg.MaxFields)
	}

	p, err := m.store.GetProfile(profileID)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, ErrProfileNotFound
	}

	seenNames := make(map[string]bool)
	var validated []store.ProfileField

	for i, f := range fields {
		name, err := ValidateFieldName(f.FieldName)
		if err != nil {
			return nil, err
		}
		lowerName := strings.ToLower(name)
		if seenNames[lowerName] {
			return nil, ErrDuplicateFieldName
		}
		seenNames[lowerName] = true

		cleanCSV, _, err := ValidateKeywordsCSV(f.KeywordsCSV)
		if err != nil {
			return nil, err
		}

		f.ProfileID = profileID
		f.FieldName = name
		f.KeywordsCSV = cleanCSV
		if f.PriorityOrder <= 0 {
			f.PriorityOrder = i + 1
		}
		validated = append(validated, f)
	}

	return m.store.ReplaceProfileFields(profileID, validated)
}

