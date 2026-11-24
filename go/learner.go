package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var learnerWordRe = regexp.MustCompile(`[A-Za-z0-9][A-Za-z0-9'_-]{1,31}`)

var learnerStopwords = map[string]struct{}{
	"the": {}, "and": {}, "for": {}, "that": {}, "this": {}, "with": {}, "from": {},
	"they": {}, "have": {}, "was": {}, "were": {}, "are": {}, "you": {}, "your": {},
	"not": {}, "but": {}, "what": {}, "all": {}, "can": {}, "had": {}, "her": {},
	"his": {}, "one": {}, "our": {}, "out": {}, "has": {}, "she": {}, "him": {},
}

// evdev keycodes -> unshifted / shifted printable characters
var evdevCharTable = map[uint16][2]rune{
	2: {'1', '!'}, 3: {'2', '@'}, 4: {'3', '#'}, 5: {'4', '$'}, 6: {'5', '%'},
	7: {'6', '^'}, 8: {'7', '&'}, 9: {'8', '*'}, 10: {'9', '('}, 11: {'0', ')'},
	12: {'-', '_'}, 13: {'=', '+'},
	16: {'q', 'Q'}, 17: {'w', 'W'}, 18: {'e', 'E'}, 19: {'r', 'R'}, 20: {'t', 'T'},
	21: {'y', 'Y'}, 22: {'u', 'U'}, 23: {'i', 'I'}, 24: {'o', 'O'}, 25: {'p', 'P'},
	30: {'a', 'A'}, 31: {'s', 'S'}, 32: {'d', 'D'}, 33: {'f', 'F'}, 34: {'g', 'G'},
	35: {'h', 'H'}, 36: {'j', 'J'}, 37: {'k', 'K'}, 38: {'l', 'L'}, 39: {';', ':'},
	40: {'\'', '"'},
	44: {'z', 'Z'}, 45: {'x', 'X'}, 46: {'c', 'C'}, 47: {'v', 'V'}, 48: {'b', 'B'},
	49: {'n', 'N'}, 50: {'m', 'M'}, 51: {',', '<'}, 52: {'.', '>'}, 53: {'/', '?'},
	57: {' ', ' '},
}

const (
	evKeyEsc        = 1
	evKeyBackspace  = 14
	evKeyTab        = 15
	evKeyEnter      = 28
	evKeyLeftShift  = 42
	evKeyRightShift = 54
	evKeySpace      = 57
	evKeyKpEnter    = 96
)

func isWordCharRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '\'' || r == '_' || r == '-'
}

func extractWordPairFromTail(committed string, backspaces int, typed string) (string, string, bool) {
	if backspaces <= 0 || strings.TrimSpace(typed) == "" {
		return "", "", false
	}
	runes := []rune(committed)
	if backspaces < len(runes) {
		prev := runes[len(runes)-backspaces-1]
		if isWordCharRune(prev) {
			return "", "", false
		}
	}
	var deleted string
	if backspaces <= len(runes) {
		deleted = string(runes[len(runes)-backspaces:])
	} else {
		deleted = committed
	}
	oldWords := learnerWordRe.FindAllString(deleted, -1)
	newWords := learnerWordRe.FindAllString(typed, -1)
	if len(newWords) == 0 {
		return "", "", false
	}
	for len(oldWords) > 0 && len(newWords) > 0 && oldWords[len(oldWords)-1] == newWords[len(newWords)-1] {
		oldWords = oldWords[:len(oldWords)-1]
		newWords = newWords[:len(newWords)-1]
	}
	for len(oldWords) > 0 && len(newWords) > 0 && oldWords[0] == newWords[0] {
		oldWords = oldWords[1:]
		newWords = newWords[1:]
	}
	if len(oldWords) == 1 && len(newWords) == 1 && oldWords[0] != newWords[0] {
		return oldWords[0], newWords[0], true
	}
	return "", "", false
}

// sequenceMatchCount computes total matching characters across recursive longest-common-substrings (Ratcliff/Obershelp).
func sequenceMatchCount(a, b []rune) int {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	bestLen, bestI, bestJ := 0, 0, 0
	dp := make([]int, len(b)+1)
	for i := 1; i <= len(a); i++ {
		prev := 0
		for j := 1; j <= len(b); j++ {
			cur := dp[j]
			if a[i-1] == b[j-1] {
				dp[j] = prev + 1
				if dp[j] > bestLen {
					bestLen = dp[j]
					bestI = i - bestLen
					bestJ = j - bestLen
				}
			} else {
				dp[j] = 0
			}
			prev = cur
		}
	}
	if bestLen == 0 {
		return 0
	}
	total := bestLen
	total += sequenceMatchCount(a[:bestI], b[:bestJ])
	total += sequenceMatchCount(a[bestI+bestLen:], b[bestJ+bestLen:])
	return total
}

func sequenceRatio(a, b string) float64 {
	ra, rb := []rune(strings.ToLower(a)), []rune(strings.ToLower(b))
	if len(ra)+len(rb) == 0 {
		return 1.0
	}
	return 2.0 * float64(sequenceMatchCount(ra, rb)) / float64(len(ra)+len(rb))
}

func fuzzyMatchInCommitted(committed, newWord string) string {
	candidates := learnerWordRe.FindAllString(committed, -1)
	bestWord := ""
	bestScore := 0.0
	for _, cand := range candidates {
		if cand == newWord {
			continue
		}
		score := sequenceRatio(cand, newWord)
		if score > bestScore {
			bestScore = score
			bestWord = cand
		}
	}
	if bestWord != "" && bestScore >= 0.55 {
		return bestWord
	}
	return ""
}

func saveLearnedReplacement(oldWord, newWord string) bool {
	if oldWord == "" || newWord == "" || oldWord == newWord {
		return false
	}
	key := strings.ToLower(strings.TrimSpace(oldWord))
	if key == "" {
		return false
	}
	if _, stop := learnerStopwords[key]; stop {
		return false
	}

	path := configFilePath()
	doc := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &doc)
	}
	dict, _ := doc["custom_dictionary"].(map[string]any)
	if dict == nil {
		dict = map[string]any{}
	}
	if cur, ok := dict[key].(string); ok && cur == newWord {
		return false
	}
	dict[key] = newWord
	doc["custom_dictionary"] = dict

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false
	}
	encoded, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return false
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		return false
	}
	log.Printf("[LEARNER] Auto-learned dictionary rule: %q -> %q", key, newWord)
	return true
}

type EditTracker struct {
	mu            sync.Mutex
	onLearn       func(oldWord, newWord string)
	window        time.Duration
	lastCommitted string
	lastCommitAt  time.Time
	backspaces    int
	typed         []rune
	shiftHeld     bool
}

func NewEditTracker(onLearn func(oldWord, newWord string), window time.Duration) *EditTracker {
	if window <= 0 {
		window = 20 * time.Second
	}
	return &EditTracker{
		onLearn: onLearn,
		window:  window,
	}
}

var globalEditTracker = NewEditTracker(func(oldWord, newWord string) {
	saveLearnedReplacement(oldWord, newWord)
}, 20*time.Second)

func (e *EditTracker) OnCommit(text string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.lastCommitted = strings.TrimSpace(text)
	e.lastCommitAt = time.Now()
	e.backspaces = 0
	e.typed = nil
}

func (e *EditTracker) resetLocked() {
	e.lastCommitted = ""
	e.backspaces = 0
	e.typed = nil
}

func (e *EditTracker) OnEvdevKey(evCode uint16, evVal int32) (string, string, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if evCode == evKeyLeftShift || evCode == evKeyRightShift {
		e.shiftHeld = evVal != 0
		return "", "", false
	}
	if evVal == 0 {
		return "", "", false
	}
	if e.lastCommitted == "" {
		return "", "", false
	}
	if time.Since(e.lastCommitAt) > e.window {
		e.resetLocked()
		return "", "", false
	}
	if evCode == evKeyEsc {
		e.resetLocked()
		return "", "", false
	}
	if evCode == evKeyBackspace {
		if len(e.typed) > 0 {
			e.typed = e.typed[:len(e.typed)-1]
		} else {
			e.backspaces++
		}
		return "", "", false
	}

	pair, ok := evdevCharTable[evCode]
	var ch rune
	if ok {
		if e.shiftHeld {
			ch = pair[1]
		} else {
			ch = pair[0]
		}
	}

	if evCode == evKeyEnter || evCode == evKeyKpEnter || evCode == evKeyTab ||
		evCode == evKeySpace || ch == '.' || ch == ',' || ch == '!' || ch == '?' {
		return e.finalizeLocked()
	}

	if ch >= 32 && ch <= 126 && e.backspaces >= 2 {
		e.typed = append(e.typed, ch)
	}
	return "", "", false
}

func (e *EditTracker) finalizeLocked() (string, string, bool) {
	if e.backspaces < 2 || len(e.typed) == 0 {
		e.resetLocked()
		return "", "", false
	}
	typedStr := string(e.typed)
	committed := e.lastCommitted
	bs := e.backspaces
	e.resetLocked()

	oldW, newW, found := extractWordPairFromTail(committed, bs, typedStr)
	if !found {
		newWords := learnerWordRe.FindAllString(typedStr, -1)
		if len(newWords) == 1 {
			if match := fuzzyMatchInCommitted(committed, newWords[0]); match != "" {
				oldW, newW, found = match, newWords[0], true
			}
		}
	}
	if found && e.onLearn != nil {
		e.onLearn(oldW, newW)
	}
	return oldW, newW, found
}
