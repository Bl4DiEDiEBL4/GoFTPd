package zipscript

type Config struct {
	Enabled bool `yaml:"enabled"`
	Debug   bool `yaml:"debug"`

	Sections     SectionsConfig     `yaml:"sections"`
	Race         RaceConfig         `yaml:"race"`
	SFV          SFVConfig          `yaml:"sfv"`
	Incomplete   IncompleteConfig   `yaml:"incomplete"`
	AllowedFiles AllowedFilesConfig `yaml:"allowed_files"`
	Audio        AudioConfig        `yaml:"audio"`

	// Legacy flat keys kept for compatibility with the first pass.
	LegacyRaceStats          bool `yaml:"race_stats"`
	LegacyCompleteBanner     bool `yaml:"complete_banner"`
	LegacyMusicCompleteGenre bool `yaml:"music_complete_genre"`
	LegacyDeleteBadCRC       bool `yaml:"delete_bad_crc"`
	LegacyIgnoreZeroSize     bool `yaml:"ignore_zero_size"`
}

type SectionsConfig struct {
	SFV     []string `yaml:"sfv"`
	NoCheck []string `yaml:"nocheck"`
	Cleanup []string `yaml:"cleanup"`
}

type RaceConfig struct {
	Enabled            bool `yaml:"enabled"`
	MaxUsersInTop      int  `yaml:"max_users_in_top"`
	MaxGroupsInTop     int  `yaml:"max_groups_in_top"`
	CompleteBanner     bool `yaml:"complete_banner"`
	MusicCompleteGenre bool `yaml:"music_complete_genre"`
	AnnounceNoRace     bool `yaml:"announce_norace"`
}

type SFVConfig struct {
	ForceFirst     bool `yaml:"force_first"`
	DenyDoubleSFV  bool `yaml:"deny_double_sfv"`
	DeleteBadCRC   bool `yaml:"delete_bad_crc"`
	IgnoreZeroSize bool `yaml:"ignore_zero_size"`
	AllowResume    bool `yaml:"allow_resume"`
}

type IncompleteConfig struct {
	Enabled               bool   `yaml:"enabled"`
	Indicator             string `yaml:"indicator"`
	NFOIndicator          string `yaml:"nfo_indicator"`
	CDIndicator           string `yaml:"cd_indicator"`
	MarkEmptyDirsOnRescan bool   `yaml:"mark_empty_dirs_on_rescan"`
}

type AllowedFilesConfig struct {
	AllowedTypes []string `yaml:"allowed_types"`
	IgnoredTypes []string `yaml:"ignored_types"`
}

type AudioConfig struct {
	Enabled                 bool            `yaml:"enabled"`
	CBRCheck                bool            `yaml:"cbr_check"`
	YearCheck               bool            `yaml:"year_check"`
	BannedGenreCheck        bool            `yaml:"banned_genre_check"`
	AllowedGenreCheck       bool            `yaml:"allowed_genre_check"`
	AllowedConstantBitrates []int           `yaml:"allowed_constant_bitrates"`
	AllowedYears            []int           `yaml:"allowed_years"`
	BannedGenres            []string        `yaml:"banned_genres"`
	AllowedGenres           []string        `yaml:"allowed_genres"`
	GenrePath               string          `yaml:"genre_path"`
	ArtistPath              string          `yaml:"artist_path"`
	YearPath                string          `yaml:"year_path"`
	GroupPath               string          `yaml:"group_path"`
	Sort                    AudioSortConfig `yaml:"sort"`
}

type AudioSortConfig struct {
	Genre  bool `yaml:"genre"`
	Artist bool `yaml:"artist"`
	Year   bool `yaml:"year"`
	Group  bool `yaml:"group"`
}

func (c *Config) ApplyDefaults() {
	if c == nil {
		return
	}

	// Map legacy flat keys into the nested layout.
	if c.LegacyRaceStats {
		c.Race.Enabled = true
	}
	if c.LegacyCompleteBanner {
		c.Race.CompleteBanner = true
	}
	if c.LegacyMusicCompleteGenre {
		c.Race.MusicCompleteGenre = true
	}
	if c.LegacyDeleteBadCRC {
		c.SFV.DeleteBadCRC = true
	}
	if c.LegacyIgnoreZeroSize {
		c.SFV.IgnoreZeroSize = true
	}

	// Preserve current behavior unless explicitly configured otherwise.
	if !c.Enabled &&
		!c.Race.Enabled &&
		!c.Race.CompleteBanner &&
		!c.Race.MusicCompleteGenre &&
		!c.SFV.DeleteBadCRC &&
		!c.SFV.IgnoreZeroSize {
		c.Enabled = true
		c.Race.Enabled = true
		c.Race.CompleteBanner = true
		c.Race.MusicCompleteGenre = true
		c.SFV.DeleteBadCRC = true
		c.SFV.IgnoreZeroSize = true
	}

	if c.Enabled &&
		!c.Race.Enabled &&
		!c.Race.CompleteBanner &&
		!c.Race.MusicCompleteGenre &&
		!c.SFV.DeleteBadCRC &&
		!c.SFV.IgnoreZeroSize {
		c.Race.Enabled = true
		c.Race.CompleteBanner = true
		c.Race.MusicCompleteGenre = true
		c.SFV.DeleteBadCRC = true
		c.SFV.IgnoreZeroSize = true
	}

	if c.Race.Enabled {
		if !c.Race.CompleteBanner {
			c.Race.CompleteBanner = true
		}
		if !c.Race.MusicCompleteGenre {
			c.Race.MusicCompleteGenre = true
		}
	}
}
