package types

import (
	common "github.com/automatedhome/common/pkg/types"
)

type Status struct {
	Mode  string `json:"mode"`
	Since int64  `json:"since"`
}

type Settings struct {
	SolarCritical common.DataPoint `yaml:"solarCritical"`
	SolarOn       common.DataPoint `yaml:"solarOn"`
	SolarOff      common.DataPoint `yaml:"solarOff"`
	TankMax       common.DataPoint `yaml:"tankMax"`
	Flow          struct {
		DutyMin common.DataPoint `yaml:"dutyMin"`
		TempMin common.DataPoint `yaml:"tempMin"`
		DutyMax common.DataPoint `yaml:"dutyMax"`
		TempMax common.DataPoint `yaml:"tempMax"`
	} `yaml:"flow"`
}

type Sensors struct {
	SolarUp  EvokDevice `yaml:"solarUp"`
	SolarIn  EvokDevice `yaml:"solarIn"`
	SolarOut EvokDevice `yaml:"solarOut"`
	TankUp   EvokDevice `yaml:"tankUp"`
}

type Actuators struct {
	Pump string `yaml:"pump"`
	Sw   string `yaml:"switch"`
	Flow string `yaml:"flow"`
}

type Config struct {
	//ReducedTime float64   `yaml:"reduced_heat_exchange"`
	Actuators Actuators `yaml:"actuators"`
	Sensors   Sensors   `yaml:"sensors"`
	Settings  Settings  `yaml:"settings"`
}

type EvokDevice struct {
	Value   float64 `json:"value,omitempty"`
	Circuit string  `json:"circuit"`
	Dev     string  `json:"dev"`
}
