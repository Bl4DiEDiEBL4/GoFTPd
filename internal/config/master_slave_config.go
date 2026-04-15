package config

// MasterSlaveConfig holds the master/slave related configuration
// embedded inside the main Config struct.

// SlaveDefinition is a configured slave stored on the master side (/slaves/*.json)
type SlaveDefinition struct {
	Name     string   `yaml:"name"`
	Masks    []string `yaml:"masks"`    // allowed IP masks, e.g. "192.168.1.*"
	PasvAddr string   `yaml:"pasv_addr"` // override PASV address for this slave
	Keywords []string `yaml:"keywords"`
}
