//go:build !windows

package bot

import (
	"bufio"
	"log"
	"os"
	"time"
)

func (b *Bot) readEvents() {
	log.Printf("[Bot] FIFO reader starting, watching %s", b.Config.EventFIFO)
	waited := false
	for {
		for {
			if _, err := os.Stat(b.Config.EventFIFO); err == nil {
				if waited {
					log.Printf("[Bot] FIFO appeared at %s", b.Config.EventFIFO)
				}
				break
			}
			if !waited {
				log.Printf("[Bot] FIFO not present at %s, waiting...", b.Config.EventFIFO)
				waited = true
			}
			time.Sleep(time.Second)
		}
		f, err := os.Open(b.Config.EventFIFO)
		if err != nil {
			log.Printf("[Bot] Failed to open FIFO: %v", err)
			time.Sleep(2 * time.Second)
			continue
		}
		log.Printf("[Bot] FIFO opened, reading events from %s", b.Config.EventFIFO)
		s := bufio.NewScanner(f)
		for s.Scan() {
			if !b.publishEventLine(s.Text()) {
				_ = f.Close()
				return
			}
		}
		if err := s.Err(); err != nil {
			log.Printf("[Bot] FIFO scanner error: %v", err)
		} else {
			log.Printf("[Bot] FIFO writer closed (EOF), reopening...")
		}
		_ = f.Close()
		waited = false
	}
}
