package vecindex

type Mode string

const (
	ModeReadWrite Mode = "read-write"
	ModeReadOnly  Mode = "read-only"
	ModeDisabled  Mode = "disabled"
)

type Health string

const (
	HealthHealthy   Health = "healthy"
	HealthReadOnly  Health = "read_only"
	HealthDisabled  Health = "disabled"
	HealthUnhealthy Health = "unhealthy"
)

type Capability struct {
	Backend       string `json:"backend"`
	Mode          Mode   `json:"mode"`
	Status        Health `json:"status"`
	ReadAvailable bool   `json:"read_available"`
	WriteSafe     bool   `json:"write_safe"`
	LastSelfTest  string `json:"last_self_test,omitempty"`
	Detail        string `json:"detail,omitempty"`
}

func DisabledCapability(detail string) Capability {
	return Capability{
		Backend:       "none",
		Mode:          ModeDisabled,
		Status:        HealthDisabled,
		ReadAvailable: false,
		WriteSafe:     false,
		Detail:        detail,
	}
}
