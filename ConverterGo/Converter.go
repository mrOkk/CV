package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

//go:embed default.css
var defaultCSS string

type options struct {
	inputPath  string
	outputPath string
	cssPath    string
	title      string
}

const pageBreakMarker = "<----->"

func main() {
	opts, err := parseOptions(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	if err := run(opts); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	fmt.Printf("PDF created: %s\n", opts.outputPath)
}

func parseOptions(args []string) (options, error) {
	fs := flag.NewFlagSet("md2pdf", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var opts options
	fs.StringVar(&opts.inputPath, "in", "", "input markdown file (.md)")
	fs.StringVar(&opts.outputPath, "out", "", "output pdf file (.pdf)")
	fs.StringVar(&opts.cssPath, "css", "", "optional css file path")
	fs.StringVar(&opts.title, "title", "", "optional document title")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return options{}, flag.ErrHelp
		}
		return options{}, err
	}

	// Allow positional usage: md2pdf input.md output.pdf
	extra := fs.Args()
	if opts.inputPath == "" && len(extra) > 0 {
		opts.inputPath = extra[0]
	}
	if opts.outputPath == "" && len(extra) > 1 {
		opts.outputPath = extra[1]
	}

	if opts.inputPath == "" {
		printUsage(fs)
		return options{}, errors.New("input file is required")
	}

	if opts.outputPath == "" {
		ext := filepath.Ext(opts.inputPath)
		base := strings.TrimSuffix(opts.inputPath, ext)
		opts.outputPath = base + ".pdf"
	}

	absIn, err := filepath.Abs(opts.inputPath)
	if err == nil {
		opts.inputPath = absIn
	}

	absOut, err := filepath.Abs(opts.outputPath)
	if err == nil {
		opts.outputPath = absOut
	}

	if opts.cssPath != "" {
		absCSS, err := filepath.Abs(opts.cssPath)
		if err == nil {
			opts.cssPath = absCSS
		}
	}

	return opts, nil
}

func printUsage(fs *flag.FlagSet) {
	fmt.Fprintln(os.Stderr, "Usage:")
	fmt.Fprintln(os.Stderr, "  md2pdf -in input.md [-out output.pdf] [-css styles.css] [-title \"Doc\"]")
	fmt.Fprintln(os.Stderr, "  md2pdf input.md [output.pdf]")
	fmt.Fprintln(os.Stderr)
	fs.PrintDefaults()
}

func run(opts options) error {
	markdownData, err := os.ReadFile(opts.inputPath)
	if err != nil {
		return fmt.Errorf("read input markdown: %w", err)
	}
	if len(bytes.TrimSpace(markdownData)) == 0 {
		return errors.New("input markdown is empty")
	}

	cssData := []byte(defaultCSS)
	if opts.cssPath != "" {
		cssData, err = os.ReadFile(opts.cssPath)
		if err != nil {
			return fmt.Errorf("read css file: %w", err)
		}
	}

	htmlBody, err := markdownToHTML(markdownData)
	if err != nil {
		return fmt.Errorf("convert markdown to html: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(opts.outputPath), 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}

	docTitle := opts.title
	if docTitle == "" {
		docTitle = strings.TrimSuffix(filepath.Base(opts.inputPath), filepath.Ext(opts.inputPath))
	}

	fullHTML := buildHTMLDocument(docTitle, string(cssData), htmlBody)
	if err := renderPDF(fullHTML, opts.outputPath); err != nil {
		return fmt.Errorf("render pdf: %w", err)
	}

	return nil
}

func markdownToHTML(markdown []byte) (string, error) {
	parts := splitMarkdownByPageBreakMarker(markdown)
	if len(parts) == 0 {
		return "", nil
	}

	md := goldmark.New(
		goldmark.WithExtensions(
			extension.GFM,
			extension.Footnote,
			extension.DefinitionList,
			extension.Typographer,
		),
	)

	htmlParts := make([]string, 0, len(parts))
	for _, part := range parts {
		var out bytes.Buffer
		if err := md.Convert(part, &out); err != nil {
			return "", err
		}
		htmlParts = append(htmlParts, out.String())
	}

	return strings.Join(htmlParts, "\n<div class=\"page-break\"></div>\n"), nil
}

func splitMarkdownByPageBreakMarker(markdown []byte) [][]byte {
	normalized := strings.ReplaceAll(string(markdown), "\r\n", "\n")
	lines := strings.Split(normalized, "\n")

	parts := make([][]byte, 0, 2)
	var current strings.Builder

	flush := func() {
		parts = append(parts, []byte(current.String()))
		current.Reset()
	}

	for i, line := range lines {
		if strings.TrimSpace(line) == pageBreakMarker {
			flush()
			continue
		}

		if current.Len() > 0 {
			current.WriteByte('\n')
		}
		current.WriteString(line)

		// Keep trailing newline semantics of the last line when present.
		if i == len(lines)-1 && strings.HasSuffix(normalized, "\n") {
			current.WriteByte('\n')
		}
	}

	flush()
	return parts
}

func buildHTMLDocument(title, css, body string) string {
	escapedTitle := html.EscapeString(title)
	return "<!doctype html><html><head><meta charset=\"utf-8\"><title>" + escapedTitle + "</title><style>" + css + "</style></head><body>" + body + "</body></html>"
}

func renderPDF(documentHTML, outputPath string) error {
	browserPath, err := findBrowserExecutable()
	if err != nil {
		return err
	}

	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(browserPath),
	)
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), allocOpts...)
	defer allocCancel()

	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	timeoutCtx, timeoutCancel := context.WithTimeout(ctx, 45*time.Second)
	defer timeoutCancel()

	var pdfData []byte
	htmlURL := "data:text/html;charset=utf-8;base64," + base64.StdEncoding.EncodeToString([]byte(documentHTML))

	err = chromedp.Run(timeoutCtx,
		chromedp.Navigate(htmlURL),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.ActionFunc(func(ctx context.Context) error {
			data, _, err := page.PrintToPDF().
				WithPrintBackground(true).
				WithPaperWidth(8.27).
				WithPaperHeight(11.69).
				WithMarginTop(0).
				WithMarginBottom(0).
				WithMarginLeft(0).
				WithMarginRight(0).
				Do(ctx)
			if err != nil {
				return err
			}
			pdfData = data
			return nil
		}),
	)
	if err != nil {
		return err
	}

	if len(pdfData) == 0 {
		return errors.New("received empty pdf data")
	}

	if err := os.WriteFile(outputPath, pdfData, 0o644); err != nil {
		return fmt.Errorf("write output file: %w", err)
	}
	return nil
}

func findBrowserExecutable() (string, error) {
	if fromEnv := strings.TrimSpace(os.Getenv("CHROME_PATH")); fromEnv != "" {
		if _, err := os.Stat(fromEnv); err == nil {
			return fromEnv, nil
		}
	}

	candidates := []string{
		"chrome",
		"msedge",
		"chromium",
		filepath.Join(os.Getenv("ProgramFiles"), "Google", "Chrome", "Application", "chrome.exe"),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "Google", "Chrome", "Application", "chrome.exe"),
		filepath.Join(os.Getenv("LocalAppData"), "Google", "Chrome", "Application", "chrome.exe"),
		filepath.Join(os.Getenv("ProgramFiles"), "Microsoft", "Edge", "Application", "msedge.exe"),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "Microsoft", "Edge", "Application", "msedge.exe"),
		filepath.Join(os.Getenv("LocalAppData"), "Microsoft", "Edge", "Application", "msedge.exe"),
	}

	for _, c := range candidates {
		if strings.TrimSpace(c) == "" {
			continue
		}
		if path, err := exec.LookPath(c); err == nil {
			return path, nil
		}
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}

	return "", errors.New("browser not found. Install Google Chrome/Microsoft Edge or set CHROME_PATH")
}
