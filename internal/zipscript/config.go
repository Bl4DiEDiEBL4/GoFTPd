package zipscript

type Config struct {
	Enabled            bool `yaml:"enabled"`
	RaceStats          bool `yaml:"race_stats"`
	CompleteBanner     bool `yaml:"complete_banner"`
	MusicCompleteGenre bool `yaml:"music_complete_genre"`
	DeleteBadCRC       bool `yaml:"delete_bad_crc"`
	IgnoreZeroSize     bool `yaml:"ignore_zero_size"`
}

func (c *Config) ApplyDefaults() {
	if c == nil {
		return
	}
	// Preserve current behavior unless explicitly disabled.
	if !c.Enabled && !c.RaceStats && !c.CompleteBanner && !c.MusicCompleteGenre && !c.DeleteBadCRC && !c.IgnoreZeroSize {
		c.Enabled = true
		c.RaceStats = true
		c.CompleteBanner = true
		c.MusicCompleteGenre = true
		c.DeleteBadCRC = true
		c.IgnoreZeroSize = true
		return
	}
	if c.Enabled && !c.RaceStats && !c.CompleteBanner && !c.MusicCompleteGenre && !c.DeleteBadCRC && !c.IgnoreZeroSize {
		c.RaceStats = true
		c.CompleteBanner = true
		c.MusicCompleteGenre = true
		c.DeleteBadCRC = true
		c.IgnoreZeroSize = true
	}
}
