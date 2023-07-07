package types

type Config struct {
	//ReducedTime float64   `yaml:"reduced_heat_exchange"`
	Actuators Actuators `yaml:"actuators"`
	Sensors   Sensors   `yaml:"sensors"`
	Settings  Settings  `yaml:"settings"`
}

type Settings struct {
	SolarCritical HomeAssistantEntity `yaml:"solarCritical"`
	SolarOn       HomeAssistantEntity `yaml:"solarOn"`
	SolarOff      HomeAssistantEntity `yaml:"solarOff"`
	TankMax       HomeAssistantEntity `yaml:"tankMax"`
	Flow          FlowSettings        `yaml:"flow"`
}

type FlowSettings struct {
	DutyMin HomeAssistantEntity `yaml:"dutyMin"`
	TempMin HomeAssistantEntity `yaml:"tempMin"`
	DutyMax HomeAssistantEntity `yaml:"dutyMax"`
	TempMax HomeAssistantEntity `yaml:"tempMax"`
}

type Sensors struct {
	SolarUp  EvokDevice `yaml:"solarUp"`
	SolarIn  EvokDevice `yaml:"solarIn"`
	SolarOut EvokDevice `yaml:"solarOut"`
	TankUp   EvokDevice `yaml:"tankUp"`
}

type Actuators struct {
	Pump   EvokDevice `yaml:"pump"`
	Switch EvokDevice `yaml:"switch"`
	Flow   EvokDevice `yaml:"flow"`
}

type Status struct {
	Mode  string `json:"mode"`
	Since int64  `json:"since"`
}

type EvokDevice struct {
	Value   float64 `json:"value,omitempty" yaml:"value,omitempty"`
	Circuit string  `json:"circuit" yaml:"circuit"`
	Dev     string  `json:"dev" yaml:"dev"`
}

type HomeAssistantEntity struct {
	EntityID string  `json:"entity_id" yaml:"entity_id"`
	State    string  `json:"state,omitempty" yaml:"state,omitempty"`
	Value    float64 `json:"value,omitempty" yaml:"value,omitempty"`
}
