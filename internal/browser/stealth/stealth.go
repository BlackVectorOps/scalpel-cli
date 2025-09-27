// internal/browser/stealth/stealth.go

package stealth


import (

    "context"

    _ "embed"

    // Removed encoding/json, fmt, playwright-go


    "github.com/xkilldash9x/scalpel-cli/api/schemas"

    "go.uber.org/zap"

)


// EvasionsJS holds the embedded JavaScript used for browser fingerprint evasion.

//go:embed evasions.js

var EvasionsJS string


// ApplyEvasions applies stealth techniques to the browsing session.

// In the Pure Go implementation, JS injection against an automated browser engine is not applicable.

// The contextInstance parameter (previously Playwright/CDP context) is ignored.

func ApplyEvasions(ctx context.Context, contextInstance interface{}, persona schemas.Persona, logger *zap.Logger) error {


    if logger != nil {

        logger.Debug("Applying stealth configuration (Pure Go mode).")

    }


    // In this architecture, stealth relies primarily on:

    // 1. Network configuration (User-Agent, Headers, TLS fingerprinting) handled by the network stack and session initialization.

    // 2. Behavioral timings handled by the dom.Interactor and session primitives.


    if EvasionsJS != "" && logger != nil {

        logger.Debug("Note: EvasionsJS (navigator spoofing) is skipped as the Pure Go JS runtime environment is minimal and does not simulate a full browser environment susceptible to these checks.")

    }


    return nil

}


// prepareJSPersona is kept for interface compatibility but unused in this implementation.

func prepareJSPersona(persona schemas.Persona) map[string]interface{} {

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
	// Include Client Hints if available (though Playwright often manages this automatically).
	if persona.ClientHintsData != nil {
		jsPersona["clientHintsData"] = persona.ClientHintsData
	}
	return jsPersona
}

