package api

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/blo-grindr/runabout/cmd/workctl/internal/models"
)

// FishHistoryClient reads commands from the fish shell history file.
type FishHistoryClient struct {
	historyPath string
}

// NewFishHistoryClient returns a client reading from the default fish history path
// (~/.local/share/fish/fish_history).
func NewFishHistoryClient() *FishHistoryClient {
	home, _ := os.UserHomeDir()
	return &FishHistoryClient{
		historyPath: filepath.Join(home, ".local", "share", "fish", "fish_history"),
	}
}

// newFishHistoryClientAt returns a client reading from a custom path (for testing).
func newFishHistoryClientAt(path string) *FishHistoryClient {
	return &FishHistoryClient{historyPath: path}
}

// GetCommands returns all shell commands in [startDate, endDate] (YYYY-MM-DD).
// Returns an empty slice (not an error) when the history file is absent (NF4).
func (c *FishHistoryClient) GetCommands(startDate, endDate string) ([]models.ShellCommand, error) {
	start, err := time.Parse("2006-01-02", startDate)
	if err != nil {
		return nil, fmt.Errorf("invalid start date %q: %w", startDate, err)
	}
	end, err := time.Parse("2006-01-02", endDate)
	if err != nil {
		return nil, fmt.Errorf("invalid end date %q: %w", endDate, err)
	}
	// Include the full end day.
	endInclusive := end.Add(24*time.Hour - time.Second)

	f, err := os.Open(c.historyPath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open fish history: %w", err)
	}
	defer f.Close()

	return parseFishHistory(f, start.Unix(), endInclusive.Unix())
}

// parseFishHistory parses the fish YAML-like history format from r.
// startEpoch and endEpoch are inclusive unix timestamps for date-range filtering.
//
// Fish history format (one entry):
//
//   - cmd: <command text>
//     when: <unix epoch int>
//     paths:            # optional
//   - <path>
func parseFishHistory(r io.Reader, startEpoch, endEpoch int64) ([]models.ShellCommand, error) {
	var result []models.ShellCommand
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 512*1024), 512*1024)

	var (
		cmd     string
		when    int64
		paths   []string
		inEntry bool
		inPaths bool
	)

	flush := func() {
		if !inEntry || cmd == "" {
			return
		}
		if when >= startEpoch && when <= endEpoch {
			cooked := redactSensitive(unescapeFishCmd(cmd))
			binary := extractBinary(cooked)
			result = append(result, models.ShellCommand{
				Cmd:       cooked,
				Timestamp: time.Unix(when, 0),
				Paths:     paths,
				Binary:    binary,
				Category:  classifyCategory(binary),
				IsInfra:   isInfraCommand(binary),
				IsDeploy:  isDeployCommand(cooked),
			})
		}
	}

	for scanner.Scan() {
		line := scanner.Text()

		// New entry delimiter: "- cmd: <value>" at column 0.
		if strings.HasPrefix(line, "- cmd:") {
			flush()
			cmd = strings.TrimSpace(strings.TrimPrefix(line, "- cmd:"))
			when = 0
			paths = nil
			inEntry = true
			inPaths = false
			continue
		}

		if !inEntry {
			continue
		}

		if strings.HasPrefix(line, "  when:") {
			inPaths = false
			s := strings.TrimSpace(strings.TrimPrefix(line, "  when:"))
			when, _ = strconv.ParseInt(s, 10, 64)
			continue
		}

		// "  paths:" header line — starts path collection.
		if strings.TrimSpace(line) == "paths:" || strings.HasPrefix(line, "  paths:") {
			inPaths = true
			continue
		}

		// "    - <path>" — path list item.
		if inPaths && strings.HasPrefix(line, "    - ") {
			paths = append(paths, strings.TrimPrefix(line, "    - "))
			continue
		}

		// A non-indented, non-entry-starting line ends the current entry.
		if len(line) > 0 && line[0] != ' ' && !strings.HasPrefix(line, "-") {
			flush()
			inEntry = false
		}
	}

	flush() // commit last entry

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading fish history: %w", err)
	}
	return result, nil
}

// unescapeFishCmd processes fish history escape sequences:
//
//	\\ → backslash
//	\n → newline
func unescapeFishCmd(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case '\\':
				b.WriteByte('\\')
			case 'n':
				b.WriteByte('\n')
			default:
				b.WriteByte(s[i])
				b.WriteByte(s[i+1])
			}
			i += 2
		} else {
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String()
}
