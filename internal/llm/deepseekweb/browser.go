package deepseekweb

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"github.com/mxschmitt/playwright-go"
)

type browserSession struct {
	pw      *playwright.Playwright
	context playwright.BrowserContext
	page    playwright.Page
	profile *os.File
}

func launchBrowser(profileDir string, headless bool) (*browserSession, error) {
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		return nil, fmt.Errorf("create DeepSeek persistent browser profile: %w", err)
	}
	if err := os.Chmod(profileDir, 0o700); err != nil {
		return nil, fmt.Errorf("protect DeepSeek persistent browser profile: %w", err)
	}
	lockPath := profileDir + "/.seo-pipeline.lock"
	profile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open DeepSeek browser profile lock: %w", err)
	}
	if err := syscall.Flock(int(profile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = profile.Close()
		return nil, fmt.Errorf("DeepSeek browser profile is already used by another process: %w", err)
	}

	pw, err := playwright.Run()
	if err != nil {
		_ = releaseProfile(profile)
		return nil, fmt.Errorf("start Playwright for DeepSeek: %w", err)
	}
	browserContext, err := pw.Chromium.LaunchPersistentContext(profileDir, playwright.BrowserTypeLaunchPersistentContextOptions{
		Headless: playwright.Bool(headless),
	})
	if err != nil {
		_ = pw.Stop()
		_ = releaseProfile(profile)
		return nil, fmt.Errorf("launch Chromium with DeepSeek persistent profile: %w", err)
	}
	pages := browserContext.Pages()
	var page playwright.Page
	if len(pages) > 0 {
		page = pages[0]
	} else if page, err = browserContext.NewPage(); err != nil {
		_ = browserContext.Close()
		_ = pw.Stop()
		_ = releaseProfile(profile)
		return nil, fmt.Errorf("create DeepSeek page: %w", err)
	}
	page.SetDefaultTimeout(float64(defaultOperationTimeout.Milliseconds()))
	page.SetDefaultNavigationTimeout(float64(defaultOperationTimeout.Milliseconds()))
	return &browserSession{pw: pw, context: browserContext, page: page, profile: profile}, nil
}

func (s *browserSession) close() error {
	if s == nil {
		return nil
	}
	var result error
	if s.context != nil {
		result = errors.Join(result, s.context.Close())
		s.context = nil
	}
	if s.pw != nil {
		result = errors.Join(result, s.pw.Stop())
		s.pw = nil
	}
	result = errors.Join(result, releaseProfile(s.profile))
	s.profile = nil
	return result
}

func releaseProfile(profile *os.File) error {
	if profile == nil {
		return nil
	}
	var result error
	if err := syscall.Flock(int(profile.Fd()), syscall.LOCK_UN); err != nil {
		result = errors.Join(result, fmt.Errorf("unlock DeepSeek browser profile: %w", err))
	}
	if err := profile.Close(); err != nil {
		result = errors.Join(result, fmt.Errorf("close DeepSeek browser profile lock: %w", err))
	}
	return result
}
