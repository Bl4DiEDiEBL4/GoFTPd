//go:build windows

package bot

import (
	"bufio"
	"io"
	"log"
	"os"
	"strings"
	"time"
)

func (b *Bot) readEvents() {
	log.Printf("[Bot] event file tail reader starting, watching %s", b.Config.EventFIFO)
	offset := int64(-1)
	waited := false
	for {
		select {
		case <-b.Done:
			return
		default:
		}

		info, err := os.Stat(b.Config.EventFIFO)
		if err != nil {
			if !waited {
				log.Printf("[Bot] event file not present at %s, waiting...", b.Config.EventFIFO)
				waited = true
			}
			offset = -1
			time.Sleep(time.Second)
			continue
		}
		if waited {
			log.Printf("[Bot] event file appeared at %s", b.Config.EventFIFO)
			waited = false
		}
		if offset < 0 || info.Size() < offset {
			offset = info.Size()
			log.Printf("[Bot] tailing event file from offset %d", offset)
		}
		if info.Size() == offset {
			time.Sleep(500 * time.Millisecond)
			continue
		}

		nextOffset, ok := b.readEventFileFromOffset(offset)
		if !ok {
			return
		}
		offset = nextOffset
		time.Sleep(250 * time.Millisecond)
	}
}

func (b *Bot) readEventFileFromOffset(offset int64) (int64, bool) {
	f, err := os.Open(b.Config.EventFIFO)
	if err != nil {
		log.Printf("[Bot] Failed to open event file: %v", err)
		time.Sleep(2 * time.Second)
		return offset, true
	}
	defer f.Close()

	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		log.Printf("[Bot] Failed to seek event file: %v", err)
		return 0, true
	}

	r := bufio.NewReader(f)
	for {
		line, err := r.ReadString('\n')
		if line != "" {
			if !strings.HasSuffix(line, "\n") && err == io.EOF {
				return offset, true
			}
			offset += int64(len(line))
			if !b.publishEventLine(line) {
				return offset, false
			}
		}
		if err == io.EOF {
			return offset, true
		}
		if err != nil {
			log.Printf("[Bot] event file read error: %v", err)
			return offset, true
		}
	}
}
