package stealth

import (
    "context"
    _ "embed" // Import embed for go:embed directive
    "encoding/json"
    "fmt"
    "strings"

    "github.com/chromedp/cdproto/emulation"
    "github.com/chromedp/cdproto/page"
    "github.com/chromedp/chromedp"
    "github.com/xkilldash9x/scalpel-cli/api/schemas"
    "go.uber.org/zap"
)

// The ClientHints and Persona structs have been moved to api/schemas.

// EvasionsJS holds the embedded JavaScript used for browser fingerprint evasion.
// This is populated by the go:embed directive below, assuming a file named evasions.js
// exists in the same directory.
//go:embed evasions.js
var EvasionsJS string

// The DefaultPersona var has been moved to api/schemas.

// Apply returns a chromedp.Action that applies the stealth configurations.
// It now accepts the canonical schemas.Persona type.
func Apply(persona schemas.Persona, logger *zap.Logger) chromedp.Action {
    return chromedp.ActionFunc(func(ctx context.Context) error {
        if logger != nil {
            logger.Debug("Applying stealth persona and evasions.")
        }
        // Add a check in case embedding fails or the file is empty.
        if EvasionsJS == "" && logger != nil {
            logger.Warn("EvasionsJS is empty. Stealth capabilities may be reduced.")
        }
        return ApplyStealthEvasions(ctx, persona)
    })
}

// ApplyStealthEvasions injects JavaScript and configures the browser session to avoid detection.
// It now accepts the canonical schemas.Persona type.
func ApplyStealthEvasions(ctx context.Context, persona schemas.Persona) error {
    // 1. Marshal the persona into a JSON string for injection.
    // We reconstruct the nested 'screen' object to maintain compatibility with EvasionsJS
    // if it expects the original structure.
    jsPersona := map[string]interface{}{
        "userAgent":  persona.UserAgent,
        "platform":   persona.Platform,
        "languages":  persona.Languages,
        "timezoneId": persona.Timezone,
        "locale":     persona.Locale,
        "noiseSeed":  persona.NoiseSeed,
        "screen": map[string]interface{}{
            "width":       persona.Width,
            "height":      persona.Height,
            "availWidth":  persona.AvailWidth,
            "availHeight": persona.AvailHeight,
            "colorDepth":  persona.ColorDepth,
            "pixelDepth":  persona.PixelDepth,
        },
    }
    if persona.ClientHintsData != nil {
        jsPersona["clientHintsData"] = persona.ClientHintsData
    }

    personaJSON, err := json.Marshal(jsPersona)
    if err != nil {
        return fmt.Errorf("failed to marshal persona for stealth injection: %w", err)
    }

    // 2. Build the full script to be executed.
    // This injects the configuration before the main evasion logic runs.
    injectionScript := fmt.Sprintf("const SCALPEL_PERSONA = %s;", string(personaJSON))
    fullScript := injectionScript + "\n" + EvasionsJS

    var tasks chromedp.Tasks

    // 3. Add script to be evaluated on new document. This is the core of the evasion.
    // Only inject if EvasionsJS actually contains content to avoid errors.
    if EvasionsJS != "" {
        tasks = append(tasks, chromedp.ActionFunc(func(c context.Context) error {
            _, err := page.AddScriptToEvaluateOnNewDocument(fullScript).Do(c)
            return err
        }))
    }

    // 4. Set overrides that must be done via CDP commands.
    tasks = append(tasks,
        emulation.SetUserAgentOverride(persona.UserAgent).
            WithAcceptLanguage(strings.Join(persona.Languages, ",")).
            WithPlatform(persona.Platform),
    )

    if persona.Timezone != "" {
        tasks = append(tasks, emulation.SetTimezoneOverride(persona.Timezone))
    }

    if persona.Locale != "" {
        tasks = append(tasks, emulation.SetLocaleOverride().WithLocale(persona.Locale))
    }

    // 5. Device Metrics
    if persona.Width > 0 && persona.Height > 0 {
        orientationType := emulation.OrientationTypeLandscapePrimary
        angle := int64(0)
        if persona.Height > persona.Width {
            orientationType = emulation.OrientationTypePortraitPrimary
        }

        metrics := emulation.SetDeviceMetricsOverride(persona.Width, persona.Height, 1.0, persona.Mobile).
            WithScreenOrientation(&emulation.ScreenOrientation{
                Type:  orientationType,
                Angle: angle,
            }).
            WithScreenWidth(persona.Width).
            WithScreenHeight(persona.Height)

        tasks = append(tasks, metrics)
    }

    // Run all tasks.
    if err := chromedp.Run(ctx, tasks); err != nil {
        return fmt.Errorf("failed to apply stealth evasions: %w", err)
    }

    return nil
}
