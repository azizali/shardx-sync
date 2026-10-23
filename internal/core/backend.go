package core

// Row mirrors public.synced_profiles. Nullable text columns decode to "".
type Row struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Folder         string `json:"folder"`
	Version        int    `json:"version"`
	Parts          int    `json:"parts"`
	SizeBytes      int64  `json:"size_bytes"`
	Sha256         string `json:"sha256"`
	BlobPrefix     string `json:"blob_prefix"`
	UpdatedAt      string `json:"updated_at"`
	UpdatedBy      string `json:"updated_by"`
	UpdatedByEmail string `json:"updated_by_email"`
	UpdatedFrom    string `json:"updated_from"`
	UpdatedOS      string `json:"updated_os"`
	LockedBy       string `json:"locked_by"`
	LockedByEmail  string `json:"locked_by_email"`
	LockedFrom     string `json:"locked_from"`
	LockedAt       string `json:"locked_at"`
	DeletedAt      string `json:"deleted_at"`
	DeletedByEmail string `json:"deleted_by_email"`
}

// Team roles, stored in team_members.role. Admins see every profile and are the only ones who can delete for good.
const (
	RoleAdmin  = "admin"
	RoleMember = "member"
)

type CommitArgs struct {
	ID, Name, Folder, Sha256, Prefix, Machine, OS string
	Expected, Parts                               int
	Size                                          int64
	KeepLock                                      bool
}

type User struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

// Backend is everything the sync engine needs from the cloud. Cloud implements it against Neon;
// tests use an in-memory fake.
type Backend interface {
	ListProfiles() ([]Row, error)
	GetProfile(id string) (*Row, error)
	ClaimLock(id, machine string, force bool) (*Row, error)
	ReleaseLock(id, machine string, force bool) (*Row, error)
	CommitVersion(a CommitArgs) (*Row, error)
	MyRole() (string, error)
	DeleteProfile(id string) error
	Upload(key string, data []byte) error
	Download(key string) ([]byte, error)
	RemoveFolder(prefix string) error
	ListFolders(id string) ([]string, error)
	GetTeamSetting(key string) (string, bool, error)
	InsertTeamSetting(key, value string) error
}
