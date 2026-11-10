// Command readme-gifs draws the animated diagrams in the README. They are generated rather
// than screen-recorded, so they are reproducible and change with the code they describe:
// run `task readme:gifs` after changing a flow they show.
package main

import (
	"flag"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	width, height = 960, 540
	fps           = 25
	endHold       = 300 // hundredths of a second on the last frame before looping
)

type scene interface {
	duration() float64
	draw(c *canvas, t float64)
}

func main() {
	out := flag.String("out", "../../docs/media", "directory to write the GIFs to")
	only := flag.String("only", "", "render just this GIF (file name without .gif)")
	stills := flag.String("stills", "", "instead of GIFs, write PNG frames at these comma-separated times in seconds, to check a layout")
	flag.Parse()

	scenes := []struct {
		name  string
		scene scene
	}{
		{"reserve-race", reserveRace{}},
		{"tracer-bullet", tracerBullet()},
		{"journal-entry", journalEntry{}},
		{"at-least-once", atLeastOnce()},
	}
	for _, s := range scenes {
		if *only != "" && *only != s.name {
			continue
		}
		if *stills != "" {
			if err := writeStills(*out, s.name, s.scene, *stills); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			continue
		}
		path := filepath.Join(*out, s.name+".gif")
		if err := render(path, s.scene); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		info, _ := os.Stat(path)
		fmt.Printf("%s  %.1f s  %d KB\n", path, s.scene.duration(), info.Size()/1024)
	}
}

func render(path string, s scene) error {
	enc := newEncoder()
	c := newCanvas(width, height)
	frames := int(s.duration() * fps)
	for i := 0; i <= frames; i++ {
		s.draw(c, float64(i)/fps)
		enc.add(c.img, 100/fps)
	}
	enc.hold(endHold)
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := enc.write(f, width, height); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func writeStills(dir, name string, s scene, times string) error {
	c := newCanvas(width, height)
	for _, field := range strings.Split(times, ",") {
		t, err := strconv.ParseFloat(strings.TrimSpace(field), 64)
		if err != nil {
			return fmt.Errorf("stills: %w", err)
		}
		s.draw(c, t)
		path := filepath.Join(dir, fmt.Sprintf("%s-%05.1f.png", name, t))
		f, err := os.Create(path)
		if err != nil {
			return err
		}
		if err := png.Encode(f, c.img); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		fmt.Println(path)
	}
	return nil
}
