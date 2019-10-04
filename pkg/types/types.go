package types

import (
	common "github.com/automatedhome/common/pkg/types"
)

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
	SolarUp  common.DataPoint `yaml:"solarUp"`
	SolarIn  common.DataPoint `yaml:"solarIn"`
	SolarOut common.DataPoint `yaml:"solarOut"`
	TankUp   common.DataPoint `yaml:"tankUp"`
}

type Actuators struct {
	Pump string `yaml:"pump"`
	Sw   string `yaml:"switch"`
	Flow string `yaml:"flow"`
}

type Config struct {
	Actuators Actuators `yaml:"actuators"`
	Sensors   Sensors   `yaml:"sensors"`
	Settings  Settings  `yaml:"settings"`
}
