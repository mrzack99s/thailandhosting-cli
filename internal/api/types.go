package api

// DeviceCode is POST /api/v1/cli/device.
type DeviceCode struct {
	DeviceCode              string `json:"deviceCode"`
	UserCode                string `json:"userCode"`
	VerificationURL         string `json:"verificationUrl"`
	VerificationURLComplete string `json:"verificationUrlComplete"`
	Interval                int    `json:"interval"`
	ExpiresIn               int    `json:"expiresIn"`
}

// TokenResponse is POST /api/v1/cli/token.
type TokenResponse struct {
	Token     string `json:"token"`
	Email     string `json:"email"`
	AccountID string `json:"accountId"`
	ExpiresAt string `json:"expiresAt"`
}

// WhoAmI is GET /api/v1/cli/whoami.
type WhoAmI struct {
	Email     string `json:"email"`
	AccountID string `json:"accountId"`
	Kind      string `json:"kind"` // cli | api_key
	ProjectID string `json:"projectId,omitempty"`
}

// Project is an entry of GET /api/v1/projects.
type Project struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Slug         string `json:"slug"`
	Role         string `json:"role"`
	OwnerEmail   string `json:"ownerEmail"`
	ServiceCount int64  `json:"serviceCount"`
}

// ComputeClass is a pool a region offers.
type ComputeClass struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Arch  string `json:"arch"`
}

// Region is an entry of GET /api/v1/regions.
type Region struct {
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	IsDefault      bool           `json:"isDefault"`
	ComputeClasses []ComputeClass `json:"computeClasses"`
}

// Service is the subset of store.Service the CLI reads.
type Service struct {
	ID                    string   `json:"id"`
	ProjectID             string   `json:"projectId"`
	Name                  string   `json:"name"`
	Slug                  string   `json:"slug"`
	Kind                  string   `json:"kind"`
	Engine                string   `json:"engine"`
	Image                 string   `json:"image"`
	Status                string   `json:"status"`
	MinReplicas           int      `json:"minReplicas"`
	MaxReplicas           int      `json:"maxReplicas"`
	CPUQuota              int64    `json:"cpuQuota"`
	CPUBurst              int64    `json:"cpuBurst,omitempty"`
	MemoryMB              int      `json:"memoryMB"`
	ComputeClass          string   `json:"computeClass"`
	Port                  int      `json:"port"`
	Command               []string `json:"command"`
	Args                  []string `json:"args"`
	ContainerConcurrency  int      `json:"containerConcurrency"`
	RequestTimeoutSeconds int      `json:"requestTimeoutSeconds"`
	CPUAlwaysAllocated    bool     `json:"cpuAlwaysAllocated"`
	HealthCheckPath       string   `json:"healthCheckPath"`
	Ingress               string   `json:"ingress"`
	AllowUnauthenticated  bool     `json:"allowUnauthenticated"`
	ServingRevisionID     string   `json:"servingRevisionId,omitempty"`
	URL                   string   `json:"url,omitempty"`
	InvokerToken          string   `json:"invokerToken,omitempty"`
	RegionID              string   `json:"regionId"`
	CreatedAt             string   `json:"createdAt"`
	UpdatedAt             string   `json:"updatedAt"`
}

// ServiceEnv is one variable of GET /api/v1/services/{id} (secret values
// come back empty).
type ServiceEnv struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	IsSecret bool   `json:"isSecret"`
}

// ServiceDetail is GET /api/v1/services/{id}.
type ServiceDetail struct {
	Service         Service      `json:"service"`
	ServingRevision *Revision    `json:"servingRevision"`
	Envs            []ServiceEnv `json:"envs"`
}

// Revision is store.ServiceRevision.
type Revision struct {
	ID           string   `json:"id"`
	ServiceID    string   `json:"serviceId"`
	Number       int      `json:"number"`
	Name         string   `json:"name"`
	Image        string   `json:"image"`
	Port         int      `json:"port"`
	CPUQuota     int64    `json:"cpuQuota"`
	MemoryMB     int      `json:"memoryMB"`
	ComputeClass string   `json:"computeClass"`
	EnvKeys      []string `json:"envKeys"`
	SecretKeys   []string `json:"secretKeys"`
	MinReplicas  int      `json:"minReplicas"`
	MaxReplicas  int      `json:"maxReplicas"`
	Message      string   `json:"message"`
	CreatedBy    string   `json:"createdBy"`
	CreatedAt    string   `json:"createdAt"`
	// ImageRemoved: the image left the registry, so the revision cannot
	// be served again.
	ImageRemoved bool `json:"imageRemoved,omitempty"`
}

// ContainerSpec is the body of POST /api/v1/services/{id}/revisions.
// Env is left out when nil so the server keeps the current variables;
// CPUBurst is left out when nil so the server decides.
type ContainerSpec struct {
	Image                 string            `json:"image"`
	Port                  int               `json:"port"`
	CPUQuota              int64             `json:"cpuQuota"`
	CPUBurst              *int64            `json:"cpuBurst,omitempty"`
	MemoryMB              int               `json:"memoryMB"`
	ComputeClass          string            `json:"computeClass"`
	Command               []string          `json:"command"`
	Args                  []string          `json:"args"`
	Env                   map[string]string `json:"env,omitempty"`
	SecretEnvKeys         []string          `json:"secretEnvKeys,omitempty"`
	MinReplicas           *int              `json:"minReplicas"`
	MaxReplicas           int               `json:"maxReplicas"`
	ContainerConcurrency  int               `json:"containerConcurrency"`
	RequestTimeoutSeconds int               `json:"requestTimeoutSeconds"`
	CPUAlwaysAllocated    bool              `json:"cpuAlwaysAllocated"`
	HealthCheckPath       string            `json:"healthCheckPath"`
	Message               string            `json:"message,omitempty"`
}

// DeployResult is the answer of POST /api/v1/services/{id}/revisions.
type DeployResult struct {
	Service  Service  `json:"service"`
	Revision Revision `json:"revision"`
}

// CreateService is the body of POST /api/v1/services for a Container.
type CreateService struct {
	Kind                  string            `json:"kind"`
	Engine                string            `json:"engine"`
	Name                  string            `json:"name"`
	Slug                  string            `json:"slug"`
	ProjectID             string            `json:"projectId"`
	RegionID              string            `json:"regionId"`
	Image                 string            `json:"image"`
	Port                  int               `json:"port,omitempty"`
	CPUQuota              int64             `json:"cpuQuota,omitempty"`
	MemoryMB              int               `json:"memoryMB,omitempty"`
	ComputeClass          string            `json:"computeClass,omitempty"`
	Env                   map[string]string `json:"env,omitempty"`
	SecretEnvKeys         []string          `json:"secretEnvKeys,omitempty"`
	MinReplicas           *int              `json:"minReplicas,omitempty"`
	MaxReplicas           int               `json:"maxReplicas,omitempty"`
	Command               []string          `json:"command,omitempty"`
	Args                  []string          `json:"args,omitempty"`
	ContainerConcurrency  int               `json:"containerConcurrency,omitempty"`
	RequestTimeoutSeconds int               `json:"requestTimeoutSeconds,omitempty"`
	CPUAlwaysAllocated    bool              `json:"cpuAlwaysAllocated,omitempty"`
	HealthCheckPath       string            `json:"healthCheckPath,omitempty"`
	AllowUnauthenticated  *bool             `json:"allowUnauthenticated,omitempty"`
}

// LogEntry is runtime.LogEntry.
type LogEntry struct {
	Timestamp string `json:"timestamp"`
	Stream    string `json:"stream"`
	Message   string `json:"message"`
}

// Logs is GET /api/v1/services/{id}/logs.
type Logs struct {
	Entries   []LogEntry `json:"entries"`
	Available bool       `json:"available"`
}

// Source is POST /api/v1/run/sources.
type Source struct {
	SourceID  string `json:"sourceId"`
	SizeBytes int64  `json:"sizeBytes"`
}

// BuildRequest is the body of POST /api/v1/run/builds.
type BuildRequest struct {
	ProjectID    string `json:"projectId"`
	SourceID     string `json:"sourceId"`
	Repository   string `json:"repository"`
	Tag          string `json:"tag,omitempty"`
	Dockerfile   string `json:"dockerfile,omitempty"`
	RegionID     string `json:"regionId"`
	ComputeClass string `json:"computeClass,omitempty"`
}

// Build is a container build.
type Build struct {
	ID              string  `json:"id"`
	ProjectID       string  `json:"projectId"`
	Number          int     `json:"number"`
	Repository      string  `json:"repository"`
	Tag             string  `json:"tag"`
	Status          string  `json:"status"`
	Image           string  `json:"image"`
	Platform        string  `json:"platform"`
	Detected        string  `json:"detected"`
	Error           string  `json:"error"`
	CreatedAt       string  `json:"createdAt"`
	StartedAt       string  `json:"startedAt"`
	FinishedAt      string  `json:"finishedAt"`
	DurationSeconds float64 `json:"durationSeconds"`
}

// BuildLog is GET /api/v1/run/builds/{id}/log.
type BuildLog struct {
	Text   string `json:"text"`
	Offset int64  `json:"offset"`
	Done   bool   `json:"done"`
	Status string `json:"status"`
}

// Registry is GET /api/v1/run/registry.
type Registry struct {
	Host         string               `json:"host"`
	URL          string               `json:"url"`
	Namespace    string               `json:"namespace"`
	Repositories []RegistryRepository `json:"repositories"`
	UsedBytes    int64                `json:"usedBytes"`
	QuotaBytes   int64                `json:"quotaBytes"`
}

// RegistryRepository is one repository of GET /api/v1/run/registry.
type RegistryRepository struct {
	Name      string   `json:"name"`
	Image     string   `json:"image"`
	Tags      []string `json:"tags"`
	SizeBytes int64    `json:"sizeBytes"`
	UpdatedAt string   `json:"updatedAt"`
}

// RegistryImage is one image of GET /api/v1/run/registry/{repo}/images.
type RegistryImage struct {
	Digest    string   `json:"digest"`
	Image     string   `json:"image"`
	Tags      []string `json:"tags"`
	SizeBytes int64    `json:"sizeBytes"`
	Arches    []string `json:"arches"`
	CreatedAt string   `json:"createdAt"`
	// UsedBy names the Containers running it; Kept is whether a revision
	// keeps it for rollback.
	UsedBy []string `json:"usedBy"`
	Kept   bool     `json:"kept"`
}
