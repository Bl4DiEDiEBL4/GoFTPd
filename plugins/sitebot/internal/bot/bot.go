package bot

import (
	"goftpd/plugins/sitebot/internal/event"
	"goftpd/plugins/sitebot/internal/irc"
	"goftpd/plugins/sitebot/internal/plugin"
	"bufio"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

// Bot coordinates the sitebot
type Bot struct {
	Config       *Config
	IRC          *irc.Bot
	Plugins      *plugin.Manager
	EventChan    chan *event.Event
	Done         chan bool
	Mutex        sync.RWMutex
	Debug        bool
}

// NewBot creates new bot
func NewBot(cfg *Config) *Bot {
	return &Bot{
		Config:    cfg,
		Plugins:   plugin.NewManager(),
		EventChan: make(chan *event.Event, 100),
		Done:      make(chan bool),
		Debug:     cfg.Debug,
	}
}

// Start starts the bot
func (b *Bot) Start() error {
	if b.Config.Debug {
		log.Println("[Bot] Starting GoSitebot")
	}
	// Create IRC connection
	b.IRC = irc.NewBot(
		b.Config.IRC.Host,
		b.Config.IRC.Port,
		b.Config.IRC.Nick,
		b.Config.IRC.User,
		b.Config.IRC.RealName,
	)
	b.IRC.Password = b.Config.IRC.Password
	b.IRC.Debug = b.Debug
	
	// Connect to IRC
	if err := b.IRC.Connect(); err != nil {
		return fmt.Errorf("failed to connect to IRC: %w", err)
	}
	
	// Set channel encryption keys
	for chan_name, key := range b.Config.Encryption.Keys {
		if err := b.IRC.SetChannelKey(chan_name, key); err != nil {
			log.Printf("[Bot] Failed to set key for %s: %v", chan_name, err)
		}
	}
	
	// Initialize plugins
	if err := b.initializePlugins(); err != nil {
		return fmt.Errorf("failed to initialize plugins: %w", err)
	}
	
	// Start listening for IRC
	go b.listenIRC()
	
	// Start reading events from FTP
	go b.readEvents()
	
	// Start processing events
	go b.processEvents()
	
	if b.Debug {
		log.Println("[Bot] Started successfully")
	}
	
	return nil
}

// listenIRC listens to IRC server
func (b *Bot) listenIRC() {
	handler := func(line string) {
		if strings.Contains(line, "004") {
			// Connected to server, join channels
			for _, channel := range b.Config.IRC.Channels {
				b.IRC.Join(channel)
				if b.Debug {
					log.Printf("[Bot] Joined %s", channel)
				}
			}
		}
	}
	
	if err := b.IRC.Listen(handler); err != nil {
		log.Printf("[Bot] IRC listen error: %v", err)
	}
}

// readEvents reads events from named pipe
func (b *Bot) readEvents() {
	fifoPath := b.Config.EventFIFO
	
	for {
		// Wait for FIFO to exist
		for {
			if _, err := os.Stat(fifoPath); err == nil {
				break
			}
			time.Sleep(1 * time.Second)
		}
		
		file, err := os.Open(fifoPath)
		if err != nil {
			log.Printf("[Bot] Failed to open FIFO: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}
		
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := scanner.Text()
			evt, err := parseEvent(line)
			if err != nil {
				if b.Debug {
					log.Printf("[Bot] Parse error: %v", err)
				}
				continue
			}
			
			select {
			case b.EventChan <- evt:
			case <-b.Done:
				file.Close()
				return
			}
		}
		
		if err := scanner.Err(); err != nil {
			log.Printf("[Bot] Scanner error: %v", err)
		}
		
		file.Close()
	}
}

// processEvents processes events and announces them
func (b *Bot) processEvents() {
	for {
		select {
		case evt := <-b.EventChan:
			b.handleEvent(evt)
		case <-b.Done:
			return
		}
	}
}

// handleEvent handles a single event
func (b *Bot) handleEvent(evt *event.Event) {
	// Process through plugins
	outputs, err := b.Plugins.ProcessEvent(evt)
	if err != nil {
		log.Printf("[Bot] Plugin error: %v", err)
		return
	}
	
	// Send announcements to IRC
	for _, output := range outputs {
		for _, channel := range b.Config.IRC.Channels {
			if err := b.IRC.SendMessage(channel, output); err != nil {
				log.Printf("[Bot] Failed to send message: %v", err)
			}
		}
	}
}

// initializePlugins loads and initializes plugins
func (b *Bot) initializePlugins() error {
	// Add built-in announce plugin
	announce := plugin.NewAnnouncePlugin()
	if err := announce.Initialize(map[string]interface{}{"debug": b.Debug}); err != nil {
		return err
	}
	b.Plugins.Register(announce)
	
	// Add TVMaze plugin
	tvmaze := plugin.NewTVMazePlugin()
	if err := tvmaze.Initialize(b.Config.Plugins.Config); err != nil {
		if b.Debug {
			log.Printf("[Bot] TVMaze init error: %v", err)
		}
	}
	b.Plugins.Register(tvmaze)
	
	// Add IMDB plugin
	imdb := plugin.NewIMDBPlugin()
	if err := imdb.Initialize(b.Config.Plugins.Config); err != nil {
		if b.Debug {
			log.Printf("[Bot] IMDB init error: %v", err)
		}
	}
	b.Plugins.Register(imdb)
	
	if b.Debug {
		log.Printf("[Bot] Loaded %d plugins: %v", len(b.Plugins.List()), b.Plugins.List())
	}
	
	return nil
}

// Stop stops the bot
func (b *Bot) Stop() error {
	close(b.Done)
	
	// Close IRC
	if b.IRC != nil {
		b.IRC.Quit("Shutting down")
		b.IRC.Close()
	}
	
	// Close plugins
	b.Plugins.Close()
	
	if b.Debug {
		log.Println("[Bot] Stopped")
	}
	
	return nil
}

// parseEvent parses event from FIFO string
// Format: TYPE:user:group:section:filename:size:speed:path
func parseEvent(line string) (*event.Event, error) {
	parts := strings.Split(line, ":")
	if len(parts) < 4 {
		return nil, fmt.Errorf("invalid event format")
	}
	
	evtType := event.EventType(strings.ToUpper(parts[0]))
	user := parts[1]
	group := parts[2]
	section := parts[3]
	filename := ""
	if len(parts) > 4 {
		filename = parts[4]
	}
	
	evt := event.NewEvent(evtType, user, group, section, filename)
	
	// Parse additional fields if present
	if len(parts) > 5 {
		fmt.Sscanf(parts[5], "%d", &evt.Size)
	}
	if len(parts) > 6 {
		fmt.Sscanf(parts[6], "%f", &evt.Speed)
	}
	if len(parts) > 7 {
		evt.Path = parts[7]
	}
	
	return evt, nil
}
