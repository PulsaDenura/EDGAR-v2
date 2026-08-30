<img width="324" height="279" alt="image" src="https://github.com/user-attachments/assets/3af47bbc-440f-4058-9ca1-20387460b438" />


# SEC Filing Downloader (written in Go)

A fast, minimal, and **domain-correct** Go tool for downloading SEC EDGAR filings
directly from the official SEC endpoints.

Useful for feeding into LLMS and tools like NotebookLM for market research.

Designed for:
- investors
- analysts
- researchers
- open-source data pipelines

No scraping. No HTML guessing. No paid APIs.

---

## ✨ Features

- ✅ Uses **official SEC endpoints only**
- ✅ Proper **rate limiting** (SEC-compliant)
- ✅ Explicit **CIK domain modeling**
- ✅ Deterministic file naming
- ✅ Zero external dependencies
- ✅ Single-file implementation
- ✅ OSS-friendly, readable code

---

## 🔍 What This Tool Does

1. Accepts a **stock ticker** (e.g. `AAPL`)
2. Resolves the ticker → **CIK**
3. Fetches the company’s **recent filings index**
4. Downloads each filing directly from EDGAR
5. Converts them to lean LLM readable TXT files and saves them locally

---

## 🧠 Why This Exists

Many SEC tools:
- scrape HTML
- rely on brittle selectors
- silently fail on rate limits

This tool:
- models SEC rules explicitly
- makes invalid states impossible
- fails loudly and clearly
- stays close to the data source

---

## 🏗 Architecture (High-Level)
Ticker → CIK → Submissions Index → Filing URLs → Local Files

