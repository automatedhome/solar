package types

// DataPoint is a basic data float structure also containing MQTT address
type DataPoint struct {
	Value   float64 `yaml:"value,omitempty"`
	Address string  `yaml:"address"`
}

// BoolPoint is a basic boolean data structure also containing MQTT address
type BoolPoint struct {
	Value   bool   `yaml:"value,omitempty"`
	Address string `yaml:"address"`
}
