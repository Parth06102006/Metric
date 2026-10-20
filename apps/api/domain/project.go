package domain

// Project is an ingest target owned by one tenant.
type Project struct {
	ID   string `json:"project_id"`
	Name string `json:"name"`
}

// ProjectWithKey is returned exactly once, when the project is created.
// The plaintext API key is never persisted or returned again.
type ProjectWithKey struct {
	Project
	APIKey string `json:"api_key"`
}
