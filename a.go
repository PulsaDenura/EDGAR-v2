package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/k3a/html2text"
	"golang.org/x/time/rate"
)

// ──────────────────────────────────────────────────────────────────────────────
// Minimal Dark Mode theme
// ──────────────────────────────────────────────────────────────────────────────
const (
	reset      = "\033[0m"
	bold       = "\033[1m"
	dim        = "\033[2m"
	cyan       = "\033[38;5;45m"
	mutedGreen = "\033[38;5;76m"
	warmYellow = "\033[38;5;178m"
	softRed    = "\033[38;5;203m"
	gray       = "\033[90m"
)

// ──────────────────────────────────────────────────────────────────────────────
// Structs
// ──────────────────────────────────────────────────────────────────────────────
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

// ──────────────────────────────────────────────────────────────────────────────
// Constants
// ──────────────────────────────────────────────────────────────────────────────
const (
	UserAgent         = "Company SysAdmin contact@yahoo.com"
	MaxFilesToFetch   = 10
	barWidth          = 20
	MaxRetries        = 5
	DefaultRetryDelay = 5 * time.Second
)

// ──────────────────────────────────────────────────────────────────────────────
// HTTP Client & Rate Limiter (SEC-compliant)
// ──────────────────────────────────────────────────────────────────────────────
var (
	httpClient = &http.Client{
		Timeout: 45 * time.Second,
	}
	// 8 requests per second is very safe (SEC allows ~10)
	limiter = rate.NewLimiter(rate.Limit(8), 8)
)

// ──────────────────────────────────────────────────────────────────────────────
// Rate-limited request with 429 + Retry-After handling
// ──────────────────────────────────────────────────────────────────────────────
func parseRetryAfter(val string) time.Duration {
	if val == "" {
		return 0
	}
	if secs, err := strconv.Atoi(val); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(val); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
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

		if resp.StatusCode == http.StatusTooManyRequests { // 429
			delay := parseRetryAfter(resp.Header.Get("Retry-After"))
			if delay <= 0 {
				delay = DefaultRetryDelay * time.Duration(attempt+1) // back-off
			}
			resp.Body.Close()
			time.Sleep(delay)
			continue
		}

		return resp, nil // caller checks status code
	}
	return nil, fmt.Errorf("max retries exceeded after 429 responses")
}

// ──────────────────────────────────────────────────────────────────────────────
// Main
// ──────────────────────────────────────────────────────────────────────────────
func main() {
	var tickers []string

	if len(os.Args) > 1 {
		tickers = os.Args[1:]
	} else {
		var ticker string
		fmt.Print(cyan + bold + "Enter Ticker (e.g. MSFT): " + reset)
		fmt.Scanln(&ticker)
		if ticker != "" {
			tickers = append(tickers, ticker)
		}
	}

	if len(tickers) == 0 {
		fmt.Println(softRed + "No ticker provided. Exiting." + reset)
		return
	}

	for i := range tickers {
		tickers[i] = strings.ToUpper(strings.TrimSpace(tickers[i]))
	}

	fmt.Printf("\n" + warmYellow + bold + "EDGARv2" + reset + "\n")

	for _, ticker := range tickers {
		if ticker == "" {
			continue
		}
		fmt.Printf(dim+"Ticker: "+cyan+"%s%s%s\n\n", bold, ticker, reset)
		processTicker(ticker)
	}
}

func processTicker(ticker string) {
	// 1. Get CIK
	fmt.Printf(dim + "Looking up CIK... " + reset)
	CIK, err := getCIK(ticker)
	if err != nil {
		fmt.Printf("%sFailed: %v%s\n", softRed, err, reset)
		return
	}
	paddedCIK := fmt.Sprintf("%010d", CIK)
	fmt.Printf("%sOK: %s%s\n", mutedGreen, paddedCIK, reset)

	// 2. Get filings
	fmt.Printf(dim + "Fetching recent filings... " + reset)
	submissions, err := getFilings(paddedCIK)
	if err != nil {
		fmt.Printf("%sError: %v%s\n", softRed, err, reset)
		return
	}
	fmt.Printf("%sOK%s\n", mutedGreen, reset)

	// Count 10-K / 10-Q
	var count10K, count10Q int
	for _, form := range submissions.Filings.Recent.Form {
		switch form {
		case "10-K":
			count10K++
		case "10-Q":
			count10Q++
		}
	}
	fmt.Printf(dim+"Found "+cyan+"%s"+warmYellow+"%d 10-K Forms%s "+dim+"and "+warmYellow+"%s%d 10-Q Forms%s\n\n",
		bold, count10K, reset, bold, count10Q, reset)

	if count10K+count10Q == 0 {
		fmt.Println(warmYellow + "No recent 10-K or 10-Q found." + reset)
		return
	}

	// Directory
	downloadDir := "./filings_" + ticker
	if err := os.MkdirAll(downloadDir, 0755); err != nil {
		fmt.Printf("%sError creating directory %s → %v%s\n", softRed, downloadDir, err, reset)
		return
	}
	fmt.Printf(cyan+"Target: "+reset+"%s\n", downloadDir)
	fmt.Printf(cyan+"Processing up to %d recent 10-K/Q (skipping existing)\n\n"+reset, MaxFilesToFetch)

	// Build list of files to download
	var items []item
	for i, formType := range submissions.Filings.Recent.Form {
		if formType != "10-K" && formType != "10-Q" {
			continue
		}
		if len(items) >= MaxFilesToFetch {
			break
		}
		items = append(items, item{
			formType: formType,
			accNum:   submissions.Filings.Recent.AccessionNumber[i],
			docName:  submissions.Filings.Recent.PrimaryDoc[i],
			dateStr:  submissions.Filings.Recent.FilingDate[i],
		})
	}

	total := len(items)
	if total == 0 {
		fmt.Println(warmYellow + "No files to process." + reset)
		return
	}

	fmt.Printf(warmYellow+"Processing %d file(s):"+reset+"\n", total)

	spinners := []string{"▃", "▄", "▅", "▆", "▇", "█"}
	spinnerIdx := 0
	var processed, skipped, failed int
	var errors []string

	for idx, it := range items {
		percent := float64(idx) / float64(total)
		filled := int(percent * float64(barWidth))
		bar := strings.Repeat("■", filled) + strings.Repeat("□", barWidth-filled)

		fmt.Printf("\r\033[K %s %s (%s) [%s%s%s] %3.0f%%%s ",
			spinners[spinnerIdx%len(spinners)],
			it.formType,
			it.dateStr,
			mutedGreen, bar, reset,
			percent*100,
			reset)
		spinnerIdx++
		time.Sleep(80 * time.Millisecond)

		err := downloadFiling(paddedCIK, it.accNum, it.docName, it.dateStr, it.formType, downloadDir)

		// Update line with result
		var icon, color string
		if err != nil {
			if strings.Contains(err.Error(), "already exists") {
				icon = " "
				color = warmYellow
				skipped++
			} else {
				icon = " "
				color = softRed
				msg := fmt.Sprintf("%s (%s): %v", it.formType, it.dateStr, err)
				errors = append(errors, msg)
				failed++
			}
		} else {
			icon = " "
			color = mutedGreen
			processed++
		}

		fmt.Printf("\r\033[K %s%s%s %s (%s) [%s%s%s] %3.0f%%%s ",
			color, icon, reset,
			it.formType,
			it.dateStr,
			mutedGreen, bar, reset,
			percent*100,
			reset)
	}

	fmt.Printf("\r\033[K ✓ [%s] 100%% \n", strings.Repeat("■", barWidth))

	// Summary
	fmt.Println("\n" + strings.Repeat("─", 60))
	fmt.Printf("%sSummary:%s\n", cyan+bold, reset)
	fmt.Printf(" Processed: %s%d%s\n", mutedGreen, processed, reset)
	fmt.Printf(" Skipped:   %s%d%s\n", warmYellow, skipped, reset)
	fmt.Printf(" Failed:    %s%d%s\n", softRed, failed, reset)

	if len(errors) > 0 {
		fmt.Printf("\n%s%d error(s):%s\n", softRed+bold, len(errors), reset)
		for _, e := range errors {
			fmt.Printf(" • %s\n", e)
		}
	} else if processed+skipped > 0 {
		fmt.Printf("\n%s ✓ All done! %s\n", mutedGreen+bold, reset)
	}

	fmt.Printf("\nFiles saved in: %s%s%s\n", cyan, downloadDir, reset)
}

// ──────────────────────────────────────────────────────────────────────────────
// Updated helpers (now use rate limiter + retry)
// ──────────────────────────────────────────────────────────────────────────────
func getCIK(ticker string) (int, error) {
	req, err := http.NewRequest("GET", "https://www.sec.gov/files/company_tickers.json", nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", UserAgent)

	resp, err := doRateLimitedRequest(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("received status %d", resp.StatusCode)
	}

	var data TickerMap
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return 0, err
	}

	for _, c := range data {
		if strings.EqualFold(c.Ticker, ticker) {
			return c.CIK, nil
		}
	}
	return 0, fmt.Errorf("ticker %q not found", ticker)
}

func getFilings(paddedCIK string) (Submissions, error) {
	url := fmt.Sprintf("https://data.sec.gov/submissions/CIK%s.json", paddedCIK)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return Submissions{}, err
	}
	req.Header.Set("User-Agent", UserAgent)

	resp, err := doRateLimitedRequest(req)
	if err != nil {
		return Submissions{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Submissions{}, fmt.Errorf("status %d", resp.StatusCode)
	}

	var s Submissions
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return Submissions{}, err
	}
	return s, nil
}

func downloadFiling(paddedCIK, accNum, docName, date, form, dir string) error {
	cleanAcc := strings.ReplaceAll(accNum, "-", "")
	unpaddedCIK := strings.TrimLeft(paddedCIK, "0")
	url := fmt.Sprintf("https://www.sec.gov/Archives/edgar/data/%s/%s/%s", unpaddedCIK, cleanAcc, docName)

	filename := filepath.Join(dir, fmt.Sprintf("%s_%s.txt", date, form))

	if _, err := os.Stat(filename); err == nil {
		return fmt.Errorf("already exists")
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", UserAgent)

	resp, err := doRateLimitedRequest(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}

	htmlBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read body: %v", err)
	}

	text := html2text.HTML2Text(string(htmlBytes))
	if err != nil {
		return fmt.Errorf("conversion failed: %v", err)
	}

	if err := os.WriteFile(filename, []byte(text), 0644); err != nil {
		return fmt.Errorf("write failed: %v", err)
	}
	return nil
}
