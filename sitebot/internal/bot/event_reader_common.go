package bot

import (
	"log"
	"strings"
)

func (b *Bot) publishEventLine(line string) bool {
	line = strings.TrimRight(line, "\r\n")
	if strings.TrimSpace(line) == "" {
		return true
	}
	evt, err := parseEvent(line)
	if err != nil {
		log.Printf("[Bot] event parse error: %v (raw=%q)", err, line)
		return true
	}
	log.Printf("[Bot] event %s section=%s path=%s file=%s", evt.Type, evt.Section, evt.Path, evt.Filename)
	select {
	case b.EventChan <- evt:
		return true
	case <-b.Done:
		return false
	}
}
