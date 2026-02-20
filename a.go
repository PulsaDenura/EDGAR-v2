package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jaytaylor/html2text"
	"golang.org/x/time/rate"
)

// ──────────────────────────────────────────────────────────────────────────────
// Everforest  Palette
// ──────────────────────────────────────────────────────────────────────────────
const (
	reset       = "\033[0m"
	bold        = "\033[1m"
	dim         = "\033[2m"
	forestGreen = "\033[38;5;108m" // Sage green
	earthYellow = "\033[38;5;208m" // Soft orange/yellow
	aquaBlue    = "\033[38;5;109m" // Muted blue-green
	softRed     = "\033[38;5;167m" // Terracotta red
	bgGray      = "\033[38;5;243m" // Warm gray
)

type Company struct {
	CIK    int    `json:"cik_str"`
	Ticker string `json:"ticker"`
}

type TickerMap map[string]Company

type Submissions struct {
	Filings struct {
		Recent struct {
			AccessionNumber []string `json:"accessionNumber"`
			FilingDate      []string `json:"filingDate"`
			Form            []string `json:"form"`
			PrimaryDoc      []string `json:"primaryDocument"`
		} `json:"recent"`
	} `json:"filings"`
}

type item struct {
	formType string
	accNum   string
	docName  string
	dateStr  string
}

const (
	UserAgent         = "Company SysAdmin contact@yahoo.com"
	MaxFilesToFetch   = 10
	barWidth          = 20
	MaxRetries        = 5
	DefaultRetryDelay = 5 * time.Second
)

var (
	httpClient = &http.Client{Timeout: 45 * time.Second}
	limiter    = rate.NewLimiter(rate.Limit(8), 8)
)

func parseRetryAfter(val string) time.Duration {
	if val == "" {
		return 0
	}
	if secs, err := strconv.Atoi(val); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	return 0
}

func doRateLimitedRequest(req *http.Request) (*http.Response, error) {
	if err := limiter.Wait(context.Background()); err != nil {
		return nil, fmt.Errorf("rate limiter: %w", err)
	}
	for attempt := 0; attempt < MaxRetries; attempt++ {
		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			delay := parseRetryAfter(resp.Header.Get("Retry-After"))
			if delay <= 0 {
				delay = DefaultRetryDelay * time.Duration(attempt+1)
			}
			resp.Body.Close()
			time.Sleep(delay)
			continue
		}
		return resp, nil
	}
	return nil, fmt.Errorf("max retries exceeded")
}

func main() {
	var tickers []string
	if len(os.Args) > 1 {
		tickers = os.Args[1:]
	} else {
		var ticker string
		fmt.Print(aquaBlue + bold + "Enter Ticker (e.g. MSFT): " + reset)
		fmt.Scanln(&ticker)
		if ticker != "" {
			tickers = append(tickers, ticker)
		}
	}

	if len(tickers) == 0 {
		fmt.Println(softRed + "No ticker provided. Exiting." + reset)
		return
	}

	fmt.Printf("\n" + forestGreen + bold + "EDGARv2 (Evergreen Edition)" + reset + "\n")

	for _, ticker := range tickers {
		t := strings.ToUpper(strings.TrimSpace(ticker))
		if t == "" {
			continue
		}
		fmt.Printf(bgGray+"Ticker: "+aquaBlue+"%s%s%s\n\n", bold, t, reset)
		processTicker(t)
	}
}

func processTicker(ticker string) {
	fmt.Printf(bgGray + "Looking up CIK... " + reset)
	CIK, err := getCIK(ticker)
	if err != nil {
		fmt.Printf("%sFailed: %v%s\n", softRed, err, reset)
		return
	}
	paddedCIK := fmt.Sprintf("%010d", CIK)
	fmt.Printf("%sOK: %s%s\n", forestGreen, paddedCIK, reset)

	fmt.Printf(bgGray + "Fetching filings... " + reset)
	submissions, err := getFilings(paddedCIK)
	if err != nil {
		fmt.Printf("%sError: %v%s\n", softRed, err, reset)
		return
	}
	fmt.Printf("%sOK%s\n", forestGreen, reset)

	var items []item
	for i, formType := range submissions.Filings.Recent.Form {
		if (formType == "10-K" || formType == "10-Q") && len(items) < MaxFilesToFetch {
			items = append(items, item{
				formType: formType,
				accNum:   submissions.Filings.Recent.AccessionNumber[i],
				docName:  submissions.Filings.Recent.PrimaryDoc[i],
				dateStr:  submissions.Filings.Recent.FilingDate[i],
			})
		}
	}

	if len(items) == 0 {
		fmt.Println(earthYellow + "No recent 10-K/Q found." + reset)
		return
	}

	downloadDir := "./filings_" + ticker
	os.MkdirAll(downloadDir, 0755)

	fmt.Printf(earthYellow+"Processing %d file(s) into .txt..."+reset+"\n", len(items))

	spinners := []string{" ", "▂", "▃", "▄", "▅", "▆", "▇", "█"}
	for idx, it := range items {
		percent := float64(idx) / float64(len(items))
		filled := int(percent * float64(barWidth))
		bar := strings.Repeat("■", filled) + strings.Repeat(" ", barWidth-filled)

		fmt.Printf("\r\033[K %s %s (%s) [%s%s%s] %3.0f%% ",
			spinners[idx%len(spinners)], it.formType, it.dateStr, forestGreen, bar, reset, percent*100)

		err := downloadFiling(paddedCIK, it.accNum, it.docName, it.dateStr, it.formType, downloadDir)

		if err != nil && !strings.Contains(err.Error(), "exists") {
			fmt.Printf("\n%sError: %v%s\n", softRed, err, reset)
		}
	}

	fmt.Printf("\r\033[K ✓ [%s] 100%% \n", strings.Repeat("■", barWidth))
	fmt.Printf("\n%sFiles saved in: %s%s%s\n", bgGray, aquaBlue, downloadDir, reset)
}

func getCIK(ticker string) (int, error) {
	req, _ := http.NewRequest("GET", "https://www.sec.gov/files/company_tickers.json", nil)
	req.Header.Set("User-Agent", UserAgent)
	resp, err := doRateLimitedRequest(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	var data TickerMap
	json.NewDecoder(resp.Body).Decode(&data)
	for _, c := range data {
		if strings.EqualFold(c.Ticker, ticker) {
			return c.CIK, nil
		}
	}
	return 0, fmt.Errorf("ticker not found")
}

func getFilings(paddedCIK string) (Submissions, error) {
	url := fmt.Sprintf("https://data.sec.gov/submissions/CIK%s.json", paddedCIK)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", UserAgent)
	resp, err := doRateLimitedRequest(req)
	if err != nil {
		return Submissions{}, err
	}
	defer resp.Body.Close()
	var s Submissions
	json.NewDecoder(resp.Body).Decode(&s)
	return s, nil
}

func downloadFiling(paddedCIK, accNum, docName, date, form, dir string) error {
	cleanAcc := strings.ReplaceAll(accNum, "-", "")
	unpaddedCIK := strings.TrimLeft(paddedCIK, "0")
	url := fmt.Sprintf("https://www.sec.gov/Archives/edgar/data/%s/%s/%s", unpaddedCIK, cleanAcc, docName)

	filename := filepath.Join(dir, fmt.Sprintf("%s_%s.txt", date, form))

	if _, err := os.Stat(filename); err == nil {
		return nil // Skip if exists
	}

	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", UserAgent)
	resp, err := doRateLimitedRequest(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	htmlBytes, _ := io.ReadAll(resp.Body)

	// Convert with PrettyTables OFF for better LLM tokenization
	text, err := html2text.FromString(string(htmlBytes), html2text.Options{PrettyTables: false})
	if err != nil {
		return err
	}

	// 1. Clean "Non-Breaking" Spaces (SEC filings are full of these)
	text = strings.ReplaceAll(text, "\u00a0", " ")

	// 2. XBRL "Soup" Stripper
	// Removes lines starting with technical schema links (http, xbrli, etc.)
	reSoup := regexp.MustCompile(`(?m)^(http|https|xmlns|xbrli):.*$`)
	text = reSoup.ReplaceAllString(text, "")

	// 3. Fix "Drifting" Symbols (Keeps currencies and negatives connected)
	// Joins $ and ( to the numbers they belong to
	reDrift := regexp.MustCompile(`([$\(\-])\s+`)
	text = reDrift.ReplaceAllString(text, "$1")
	reParensClose := regexp.MustCompile(`\s+\)`)
	text = reParensClose.ReplaceAllString(text, ")")

	// 4. Kill lines that contain ONLY whitespace (spaces/tabs)
	// This allows the next step to catch "empty" lines that aren't actually empty.
	reOnlyWhitespace := regexp.MustCompile(`(?m)^[ \t]+$`)
	text = reOnlyWhitespace.ReplaceAllString(text, "")

	// 5. Page Number Stripping (removes standalone digits or "Page X")
	rePage := regexp.MustCompile(`(?m)^(\s*\d+\s*|\s*[Pp]age\s+\d+\s*)$`)
	text = rePage.ReplaceAllString(text, "")

	// 6. Collapse multiple newlines (3+ becomes 2)
	// NotebookLM prefers double-newlines for distinct context blocks.
	reMultiLine := regexp.MustCompile(`\n{3,}`)
	text = reMultiLine.ReplaceAllString(text, "\n\n")

	// 7. Trim leading/trailing whitespace
	text = strings.TrimSpace(text)

	// 8. Build the final output with Metadata at the TOP
	// Using a distinct header helps the AI cite its sources chronologically.
	header := fmt.Sprintf("--- METADATA ---\nCOMPANY: %s\nCIK: %s\nFORM: %s\nDATE: %s\n----------------\n\n",
		dir, paddedCIK, form, date)

	finalContent := header + text

	return os.WriteFile(filename, []byte(finalContent), 0644)
}
