package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tdewolff/minify/v2"
	"github.com/tdewolff/minify/v2/css"
	"github.com/tdewolff/minify/v2/html"
	"github.com/tdewolff/minify/v2/js"
)

func main() {
	staticDir := flag.String("static", "cmd/zero-scale-dashboard/static", "Source static directory")
	distDir := flag.String("dist", "cmd/zero-scale-dashboard/go/dist", "Target dist directory")
	flag.Parse()

	// If run from within cmd/zero-scale-dashboard/build, resolve relative paths
	if _, err := os.Stat(*staticDir); os.IsNotExist(err) {
		if _, err := os.Stat("../static"); err == nil {
			*staticDir = "../static"
			*distDir = "../go/dist"
		}
	}

	if err := os.MkdirAll(*distDir, 0755); err != nil {
		log.Fatalf("Failed to create dist directory %s: %v", *distDir, err)
	}

	m := minify.New()
	m.AddFunc("text/css", css.Minify)
	m.AddFunc("text/html", html.Minify)
	m.AddFunc("application/javascript", js.Minify)

	fmt.Println("================================================================================")
	fmt.Println("🚀 Building Zero-Scale Dashboard Static Production Bundle")
	fmt.Printf("📂 Source Static: %s\n", *staticDir)
	fmt.Printf("📦 Target Dist:   %s\n", *distDir)
	fmt.Println("================================================================================")

	// 1. Process CSS
	cssDir := filepath.Join(*staticDir, "css")
	cssEntries, err := os.ReadDir(cssDir)
	if err != nil {
		log.Fatalf("Failed to read CSS dir %s: %v", cssDir, err)
	}
	var cssFiles []string
	for _, entry := range cssEntries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".css") {
			cssFiles = append(cssFiles, filepath.Join(cssDir, entry.Name()))
		}
	}
	sort.Strings(cssFiles)

	var cssCombined strings.Builder
	for _, f := range cssFiles {
		content, err := os.ReadFile(f)
		if err != nil {
			log.Fatalf("Failed to read %s: %v", f, err)
		}
		cssCombined.Write(content)
		cssCombined.WriteString("\n")
	}

	rawCSS := cssCombined.String()
	minCSS, err := m.String("text/css", rawCSS)
	if err != nil {
		log.Fatalf("Failed to minify CSS: %v", err)
	}

	outCSSPath := filepath.Join(*distDir, "style.min.css")
	if err := os.WriteFile(outCSSPath, []byte(minCSS), 0644); err != nil {
		log.Fatalf("Failed to write %s: %v", outCSSPath, err)
	}
	printStat("CSS Bundle", len(rawCSS), len(minCSS), outCSSPath)

	// 2. Process JS
	// Order is important: dependencies first, orchestrator (app.js) last
	jsOrder := []string{
		"auth.js",
		"metrics.js",
		"efficiency.js",
		"operators.js",
		"nodes.js",
		"zeroscale.js",
		"sites.js",
		"app.js",
	}

	var jsCombined strings.Builder
	jsDir := filepath.Join(*staticDir, "js")
	for _, jsName := range jsOrder {
		fPath := filepath.Join(jsDir, jsName)
		content, err := os.ReadFile(fPath)
		if err != nil {
			log.Fatalf("Failed to read %s: %v", fPath, err)
		}
		jsCombined.Write(content)
		jsCombined.WriteString("\n;\n")
	}

	rawJS := jsCombined.String()
	minJS, err := m.String("application/javascript", rawJS)
	if err != nil {
		log.Fatalf("Failed to minify JS: %v", err)
	}

	outJSPath := filepath.Join(*distDir, "bundle.min.js")
	if err := os.WriteFile(outJSPath, []byte(minJS), 0644); err != nil {
		log.Fatalf("Failed to write %s: %v", outJSPath, err)
	}
	printStat("JS Bundle", len(rawJS), len(minJS), outJSPath)

	// 3. Process HTML
	htmlSrcPath := filepath.Join(*staticDir, "index.html")
	rawHTMLBytes, err := os.ReadFile(htmlSrcPath)
	if err != nil {
		log.Fatalf("Failed to read %s: %v", htmlSrcPath, err)
	}
	rawHTML := string(rawHTMLBytes)
	minHTML, err := m.String("text/html", rawHTML)
	if err != nil {
		log.Fatalf("Failed to minify HTML: %v", err)
	}

	outHTMLPath := filepath.Join(*distDir, "index.html")
	if err := os.WriteFile(outHTMLPath, []byte(minHTML), 0644); err != nil {
		log.Fatalf("Failed to write %s: %v", outHTMLPath, err)
	}
	printStat("HTML Page", len(rawHTML), len(minHTML), outHTMLPath)

	fmt.Println("================================================================================")
	fmt.Println("✅ Production bundle generated successfully (zero npm/node dependencies)!")
	fmt.Println("================================================================================")
}

func printStat(name string, rawBytes, minBytes int, dest string) {
	savings := 0.0
	if rawBytes > 0 {
		savings = (1.0 - float64(minBytes)/float64(rawBytes)) * 100.0
	}
	fmt.Printf("  • %-12s: %6.1f KB -> %6.1f KB (%4.1f%% smaller) -> %s\n",
		name,
		float64(rawBytes)/1024.0,
		float64(minBytes)/1024.0,
		savings,
		dest,
	)
}
