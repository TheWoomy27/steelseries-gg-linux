package oled

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// Icons are GameSense's event icons (the SDK's icon_id), 32 pixels wide,
// read from the Engine's screen library in the imported GG data
// (game-integration/lib/screener.lsp): each is rows of four bytes.
type Icons map[int][][]byte

var iconDef = regexp.MustCompile(`\(\((\d+)\)\s*\(let \(\(spacing \(get-icon-top-bottom-spacing screen-height \d+\)\)\)\s*\(append \(make-list \(first spacing\) \[0 0 0 0\]\)\s*'\(((?:\s*\[[0-9 ]+\])+)\s*\)`)
var iconRow = regexp.MustCompile(`\[([0-9 ]+)\]`)

// LoadIcons reads the icons from screener.lsp.
func LoadIcons(path string) (Icons, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	icons := Icons{}
	for _, m := range iconDef.FindAllStringSubmatch(string(b), -1) {
		id, _ := strconv.Atoi(m[1])
		var rows [][]byte
		for _, r := range iconRow.FindAllStringSubmatch(m[2], -1) {
			var row []byte
			for _, f := range strings.Fields(r[1]) {
				v, _ := strconv.Atoi(f)
				row = append(row, byte(v))
			}
			rows = append(rows, row)
		}
		icons[id] = rows
	}
	return icons, nil
}

var (
	iconsOnce sync.Once
	iconsPath string
	loaded    Icons
)

// SetIconFile names screener.lsp for Icon.
func SetIconFile(path string) { iconsPath = path }

// Icon is an event icon's rows, or nil (no icon, or none known).
func Icon(id int) [][]byte {
	if id <= 0 {
		return nil
	}
	iconsOnce.Do(func() {
		if iconsPath != "" {
			loaded, _ = LoadIcons(iconsPath)
		}
	})
	return loaded[id]
}
