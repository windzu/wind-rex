// Mkicon renders the WindRex brand master into the desktop, iOS, and Web
// icon assets. Run from the repository root. -web limits the output to Web.
package main

import (
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/image/vector"
)

const (
	masterPath = "assets/brand/windrex.svg"
	artSize    = 200
	iconSize   = 1024
)

type iconShape int

const (
	roundedTile iconShape = iota
	squareTile
	markLayer
)

type point struct{ x, y float64 }

type brand struct {
	background color.NRGBA
	foreground color.NRGBA
	radius     float64
	stroke     float64
	points     []point
}

func main() {
	webOnly := flag.Bool("web", false, "draw only the Web icons into web/pwa")
	flag.Parse()

	master, err := os.ReadFile(masterPath)
	check(err)
	art, err := readBrand(master)
	check(err)
	art.writeWeb(master)
	if !*webOnly {
		writePNG("resources/icon.png", art.render(iconSize, 100, art.background, art.foreground, roundedTile))
		writePNG("resources/icon-dev.png", art.render(iconSize, 100, hex("3D3D70"), art.foreground, roundedTile))
		art.writeIOS()
	}
}

func readBrand(svg []byte) (brand, error) {
	var doc struct {
		XMLName xml.Name `xml:"svg"`
		ViewBox string   `xml:"viewBox,attr"`
		Tile    struct {
			Fill   string  `xml:"fill,attr"`
			Radius float64 `xml:"rx,attr"`
		} `xml:"rect"`
		Mark struct {
			Path  string  `xml:"d,attr"`
			Color string  `xml:"stroke,attr"`
			Width float64 `xml:"stroke-width,attr"`
			Cap   string  `xml:"stroke-linecap,attr"`
			Join  string  `xml:"stroke-linejoin,attr"`
		} `xml:"path"`
	}
	if err := xml.Unmarshal(svg, &doc); err != nil {
		return brand{}, err
	}
	if doc.ViewBox != "0 0 200 200" || doc.Tile.Radius <= 0 || doc.Mark.Width <= 0 ||
		doc.Mark.Cap != "round" || doc.Mark.Join != "round" {
		return brand{}, fmt.Errorf("unsupported artwork in %s", masterPath)
	}

	// The master is one move followed by absolute cubic Béziers.
	fields := strings.Fields(doc.Mark.Path)
	if len(fields) < 10 || fields[0] != "M" || (len(fields)-3)%7 != 0 {
		return brand{}, fmt.Errorf("%s: expected M followed by cubic C segments", masterPath)
	}
	start, err := readPoint(fields[1:3])
	if err != nil {
		return brand{}, err
	}
	points := []point{start}
	for i := 3; i < len(fields); i += 7 {
		if fields[i] != "C" {
			return brand{}, fmt.Errorf("%s: unsupported path command %q", masterPath, fields[i])
		}
		var controls [3]point
		for j := range controls {
			controls[j], err = readPoint(fields[i+1+j*2 : i+3+j*2])
			if err != nil {
				return brand{}, err
			}
		}
		const samples = 48
		for n := 1; n <= samples; n++ {
			t := float64(n) / samples
			u := 1 - t
			a, b, c, d := u*u*u, 3*u*u*t, 3*u*t*t, t*t*t
			points = append(points, point{
				a*start.x + b*controls[0].x + c*controls[1].x + d*controls[2].x,
				a*start.y + b*controls[0].y + c*controls[1].y + d*controls[2].y,
			})
		}
		start = controls[2]
	}
	return brand{
		background: hex(strings.TrimPrefix(doc.Tile.Fill, "#")),
		foreground: hex(strings.TrimPrefix(doc.Mark.Color, "#")),
		radius:     doc.Tile.Radius,
		stroke:     doc.Mark.Width,
		points:     points,
	}, nil
}

func readPoint(fields []string) (point, error) {
	x, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return point{}, err
	}
	y, err := strconv.ParseFloat(fields[1], 64)
	return point{x, y}, err
}

func hex(v string) color.NRGBA {
	n, err := strconv.ParseUint(v, 16, 24)
	if err != nil || len(v) != 6 {
		log.Fatalf("invalid icon color %q", v)
	}
	return color.NRGBA{R: uint8(n >> 16), G: uint8(n >> 8), B: uint8(n), A: 255}
}

// render draws a tile or a transparent mark layer. The tile and mark use
// the same scale so the proportions remain the same for platform insets.
func (b brand) render(px, inset int, bg, ink color.NRGBA, shape iconShape) image.Image {
	dst := image.NewRGBA(image.Rect(0, 0, px, px))
	scale := float64(px-2*inset) / artSize
	start, end := float32(inset), float32(px-inset)
	if shape != markLayer {
		r := vector.NewRasterizer(px, px)
		radius := float32(b.radius * scale)
		if shape == squareTile {
			radius = 0
		}
		roundRect(r, start, end, radius)
		r.Draw(dst, dst.Bounds(), image.NewUniform(bg), image.Point{})
	}

	r := vector.NewRasterizer(px, px)
	radius := b.stroke * scale / 2
	var prior point
	for i, p := range b.points {
		p = point{float64(inset) + p.x*scale, float64(inset) + p.y*scale}
		if i > 0 {
			dx, dy := p.x-prior.x, p.y-prior.y
			if distance := math.Hypot(dx, dy); distance > 0 {
				x, y := -dy*radius/distance, dx*radius/distance
				r.MoveTo(float32(prior.x-x), float32(prior.y-y))
				r.LineTo(float32(p.x-x), float32(p.y-y))
				r.LineTo(float32(p.x+x), float32(p.y+y))
				r.LineTo(float32(prior.x+x), float32(prior.y+y))
				r.ClosePath()
			}
		}
		circle(r, p, radius)
		prior = p
	}
	r.Draw(dst, dst.Bounds(), image.NewUniform(ink), image.Point{})
	return dst
}

const arc = 0.55228475

func roundRect(r *vector.Rasterizer, start, end, radius float32) {
	k := radius * arc
	r.MoveTo(start+radius, start)
	r.LineTo(end-radius, start)
	r.CubeTo(end-radius+k, start, end, start+radius-k, end, start+radius)
	r.LineTo(end, end-radius)
	r.CubeTo(end, end-radius+k, end-radius+k, end, end-radius, end)
	r.LineTo(start+radius, end)
	r.CubeTo(start+radius-k, end, start, end-radius+k, start, end-radius)
	r.LineTo(start, start+radius)
	r.CubeTo(start, start+radius-k, start+radius-k, start, start+radius, start)
	r.ClosePath()
}

func circle(r *vector.Rasterizer, p point, radius float64) {
	x, y, d, k := float32(p.x), float32(p.y), float32(radius), float32(arc*radius)
	r.MoveTo(x+d, y)
	r.CubeTo(x+d, y+k, x+k, y+d, x, y+d)
	r.CubeTo(x-k, y+d, x-d, y+k, x-d, y)
	r.CubeTo(x-d, y-k, x-k, y-d, x, y-d)
	r.CubeTo(x+k, y-d, x+d, y-k, x+d, y)
	r.ClosePath()
}

func (b brand) writeWeb(master []byte) {
	const dir = "web/pwa"
	check(os.WriteFile(filepath.Join(dir, "favicon.svg"), master, 0o644))
	for _, output := range []struct {
		name  string
		size  int
		shape iconShape
	}{
		{"icon-192.png", 192, roundedTile},
		{"icon-512.png", 512, roundedTile},
		{"maskable-192.png", 192, squareTile},
		{"maskable-512.png", 512, squareTile},
		{"apple-touch-icon.png", 180, squareTile},
	} {
		writePNG(filepath.Join(dir, output.name), b.render(output.size, 0, b.background, b.foreground, output.shape))
	}
}

func (b brand) writeIOS() {
	const ios = "ios/GoRex"
	for _, icon := range []struct {
		bundle, preview string
		background, ink color.NRGBA
	}{
		{"AppIcon", "IconWindRex", b.background, b.foreground},
		{"AppIcon-Spectrum", "IconForest", hex("23463F"), b.foreground},
		{"AppIcon-Classic", "IconPaper", b.foreground, b.background},
	} {
		dir := filepath.Join(ios, icon.bundle+".icon")
		writePNG(filepath.Join(dir, "Assets/mark.png"), b.render(iconSize, 0, icon.background, icon.ink, markLayer))
		bg := icon.background
		fill := fmt.Sprintf("srgb:%.5f,%.5f,%.5f,1.00000", float64(bg.R)/255, float64(bg.G)/255, float64(bg.B)/255)
		config := map[string]any{
			"fill": map[string]string{"solid": fill},
			"groups": []map[string]any{{
				"name":   "WindRex",
				"layers": []map[string]any{{"name": "Mark", "image-name": "mark.png", "glass": false}},
				"shadow": map[string]any{"kind": "none", "opacity": 0},
			}},
			"supported-platforms": map[string]any{"squares": []string{"iOS"}},
		}
		body, err := json.MarshalIndent(config, "", "  ")
		check(err)
		check(os.WriteFile(filepath.Join(dir, "icon.json"), append(body, '\n'), 0o644))
		writePNG(filepath.Join(ios, "Assets.xcassets", icon.preview+".imageset", "icon.png"),
			b.render(360, 0, bg, icon.ink, squareTile))
	}
	for scale, suffix := range []string{"", "@2x", "@3x"} {
		writePNG(filepath.Join(ios, "Assets.xcassets/LaunchIcon.imageset", "launch"+suffix+".png"),
			b.render(144*(scale+1), 0, b.background, b.foreground, roundedTile))
	}
	writePNG(filepath.Join(ios, "Assets.xcassets/BrandMark.imageset/mark.png"),
		b.render(240, 0, b.background, b.foreground, markLayer))
}

func writePNG(path string, img image.Image) {
	check(os.MkdirAll(filepath.Dir(path), 0o755))
	file, err := os.Create(path)
	check(err)
	err = png.Encode(file, img)
	closeErr := file.Close()
	check(err)
	check(closeErr)
}

func check(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
