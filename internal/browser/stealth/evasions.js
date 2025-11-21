// File: internal/browser/stealth/evasions.js
// This script runs in the browser context before any page scripts (via CDP Page.addScriptToEvaluateOnNewDocument).

(function() {
    'use strict';

    // Retrieve configuration injected via window.SCALPEL_PERSONA
    // NOTE: Properties are camelCase matching the Go struct JSON tags (e.g., userAgent).
    const persona = window.SCALPEL_PERSONA || {};

    // --- Utility Functions ---

    // Helper for safe property definition (getters).
    const overrideGetter = (obj, prop, value) => {
        try {
            Object.defineProperty(obj, prop, {
                get: () => value,
                configurable: true,
                enumerable: true
            });
        } catch (error) {
            // console.warn(`Scalpel Stealth: Failed to override property ${prop}`, error);
        }
    };

    // Define the function that mimics the native toString appearance globally.
    // (Fix for Bug 1: Infinite Masking)
    // We use a function expression for a slightly more authentic appearance.
    const nativeToString = function toString() { return 'function toString() { [native code] }'; };
    // Implement recursive masking (Triple Masking and beyond).
    try {
        // Self-referential toString for infinite recursion.
        Object.defineProperty(nativeToString, 'toString', {
            value: nativeToString,
            configurable: true,
        });
    } catch (e) {}


    // Utility function to make functions appear native (Advanced: Infinite Masking).
    const maskAsNative = (func, nameHint = '') => {
        try {
            const name = nameHint || func.name || '';
            const nativeString = `function ${name}() { [native code] }`;

            // Define the spoofed toString function (Masking Level 1)
            const spoofedToString = () => nativeString;

            // Advanced Evasion: Mask the toString function itself (Masking Level 2+)
            // We use the pre-defined nativeToString which handles infinite recursion.
            Object.defineProperty(spoofedToString, 'toString', {
                 value: nativeToString,
                 configurable: true,
            });

            // Override instance toString
            Object.defineProperty(func, 'toString', {
                value: spoofedToString,
                configurable: true,
            });

        } catch (e) {}
    };


    // --- Evasion: Remove webdriver flag (CRITICAL) ---
    // Defining 'webdriver' on the Navigator prototype is the most robust method.
    if (navigator.webdriver !== false) {
        overrideGetter(Navigator.prototype, 'webdriver', false);
    }
    
    // Fallback check on the instance itself.
    try {
        if (navigator.webdriver === true) {
             overrideGetter(navigator, 'webdriver', false);
        }
    } catch (error) {
        // Ignore if instance override fails.
    }

    // Apply persona configurations.
    // --- Evasion: Navigator Properties ---
    if (persona.userAgent) {
        overrideGetter(Navigator.prototype, 'userAgent', persona.userAgent);
        // Also override appVersion which is derived from UserAgent.
        overrideGetter(Navigator.prototype, 'appVersion', persona.userAgent.replace(/^Mozilla\//, ''));
    }
    if (persona.platform) {
        overrideGetter(Navigator.prototype, 'platform', persona.platform);
    }
    if (Array.isArray(persona.languages) && persona.languages.length > 0) {
        // Languages should be a frozen array to mimic native behavior.
        const languages = Object.freeze([...persona.languages]);
        overrideGetter(Navigator.prototype, 'languages', languages);
        // Also override the singular 'language' property.
        overrideGetter(Navigator.prototype, 'language', languages[0]);
    }

    // --- Evasion: Screen Properties (Advanced) ---
    if (persona.width && persona.height && window.screen) {
        overrideGetter(Screen.prototype, 'width', persona.width);
        overrideGetter(Screen.prototype, 'height', persona.height);
        
        // Use provided values or fallback to main dimensions/defaults
        const availWidth = persona.availWidth || persona.width;
        const availHeight = persona.availHeight || persona.height;

        // Robustness: Clarify ColorDepth vs DPR.
        // Persona.colorDepth is the screen color depth (e.g. 24, 32).
        // Persona.pixelDepth is used as DevicePixelRatio (DPR) in the Go CDP implementation.
        // screen.pixelDepth in JS should match screen.colorDepth.
        const colorDepth = persona.colorDepth || 24;
        const pixelDepth = colorDepth;

        overrideGetter(Screen.prototype, 'availWidth', availWidth);
        overrideGetter(Screen.prototype, 'availHeight', availHeight);
        overrideGetter(Screen.prototype, 'colorDepth', colorDepth);
        overrideGetter(Screen.prototype, 'pixelDepth', pixelDepth);
        
        // Spoof window dimensions (outerWidth/Height and innerWidth/innerHeight).
        // (Fix for Bug 3: Inconsistent Dimensions)
        try {
            if (window.outerWidth !== persona.width) {
                Object.defineProperty(window, 'outerWidth', { get: () => persona.width, configurable: true });
            }
            if (window.outerHeight !== persona.height) {
                Object.defineProperty(window, 'outerHeight', { get: () => persona.height, configurable: true });
            }
            // Also spoof innerWidth/innerHeight for consistency.
            if (window.innerWidth !== persona.width) {
                Object.defineProperty(window, 'innerWidth', { get: () => persona.width, configurable: true });
            }
            if (window.innerHeight !== persona.height) {
                Object.defineProperty(window, 'innerHeight', { get: () => persona.height, configurable: true });
            }
        } catch (e) {}
    }
    
    // --- Evasion: Basic Chrome simulation (Robustness Fix) ---
    // Headless Chrome might lack window.chrome entirely, or provide a partial object without 'runtime'.
    
    // 1. Define the core 'runtime' object and its masked functions.
    const runtimeObj = {
        connect: function connect() { return { disconnect: () => {} }; },
        sendMessage: function sendMessage() {},
        getManifest: function getManifest() { return ({}); },
        id: undefined,
    };
    maskAsNative(runtimeObj.connect);
    maskAsNative(runtimeObj.sendMessage);
    maskAsNative(runtimeObj.getManifest);

    // 2. Ensure window.chrome exists.
    if (window.chrome === undefined) {
        const appObj = { isInstalled: false, getDetails: function getDetails() { return null; } };
        maskAsNative(appObj.getDetails);

        const chromeObj = {
            runtime: runtimeObj,
            app: appObj,
            webstore: { installed: false },
        };

        Object.defineProperty(window, 'chrome', {
            value: chromeObj,
            writable: true, // Allows potential polyfills by the page itself
            configurable: true
        });
    }

    // 3. Ensure window.chrome.runtime exists (Defense in depth against partial implementations).
    if (window.chrome && window.chrome.runtime === undefined) {
         try {
            Object.defineProperty(window.chrome, 'runtime', {
                value: runtimeObj,
                writable: true,
                configurable: true,
                enumerable: true
            });
         } catch (e) {
             // Handle potential errors if window.chrome was frozen or non-configurable.
         }
    }


    // --- Evasion: Permissions API (Advanced) ---
    try {
        if (navigator.permissions && navigator.permissions.query && window.PermissionStatus) {
            const originalQuery = navigator.permissions.query;
            
            // Override the query function
            const spoofedQuery = function query(parameters) {
                // Input validation mimicking native behavior (Robustness)
                if (!parameters) {
                    throw new TypeError("Failed to execute 'query' on 'Permissions': 1 argument required, but only 0 present.");
                }
                if (typeof parameters !== 'object' || parameters === null) {
                     throw new TypeError("Failed to execute 'query' on 'Permissions': The provided value is not of type 'PermissionDescriptor'.");
                }
                if (!parameters.name) {
                    throw new TypeError("Failed to execute 'query' on 'Permissions': Failed to read the 'name' property from 'PermissionDescriptor': Required member is undefined.");
                }


                if (parameters.name === 'notifications') {
                    // Return consistent 'prompt' state.
                    // (Fix for Bug 2: Handle missing window.Notification).
                    const permissionState = window.Notification
                        ? ((Notification.permission === 'default') ? 'prompt' : Notification.permission)
                        : 'prompt';
                    
                    // (Fix for Bug 4: Detectable Object Structure)
                    // Ensure we return a genuine PermissionStatus object.
                    // Strategy: Temporarily override the prototype getter, call the original query, and restore.

                    const originalDescriptor = Object.getOwnPropertyDescriptor(PermissionStatus.prototype, 'state');
                    if (!originalDescriptor || !originalDescriptor.get) {
                        // Fallback if we can't access the descriptor (should be rare).
                        return originalQuery.call(this, parameters);
                    }

                    // Override the prototype getter temporarily.
                    Object.defineProperty(PermissionStatus.prototype, 'state', {
                        get: () => permissionState,
                        configurable: true,
                        enumerable: true
                    });

                    try {
                        // Call the original query. The resulting object will use our spoofed getter.
                        return originalQuery.call(this, parameters).then(result => {
                            // Restore the original getter immediately after the promise resolves.
                            Object.defineProperty(PermissionStatus.prototype, 'state', originalDescriptor);
                            
                            // FIX for Bug 1: Persist the spoofed state using a Proxy.
                            // Even though the prototype is restored, this Proxy ensures that accessing 'state'
                            // on THIS specific object returns the spoofed permissionState, without defining
                            // a detectable 'own property'.
                            return new Proxy(result, {
                                get: function(target, prop, receiver) {
                                    if (prop === 'state') {
                                        return permissionState;
                                    }
                                    return Reflect.get(target, prop, receiver);
                                }
                            });
                        }).catch(error => {
                            // Restore in case of error too.
                            Object.defineProperty(PermissionStatus.prototype, 'state', originalDescriptor);
                            throw error;
                        });
                    } catch (error) {
                        // Handle synchronous errors during the call (e.g. invalid parameter name).
                        Object.defineProperty(PermissionStatus.prototype, 'state', originalDescriptor);
                        throw error;
                    }
                }
                // Use the context of the navigator.permissions object
                return originalQuery.call(this, parameters);
            };

            // Masking: Make the function appear native.
            maskAsNative(spoofedQuery);

            Object.defineProperty(navigator.permissions, 'query', {
                value: spoofedQuery,
                configurable: true,
                writable: false // Make it non-writable for better stealth
            });
        }
    } catch (error) {
        // console.warn("Scalpel Stealth: Failed to spoof Permissions API", error);
    }

    // --- Evasion: WebGL (Advanced) ---
    // Spoofing of WebGL vendor/renderer if persona data is available.
    if (persona.webGLVendor && persona.webGLRenderer) {
        try {
            // Constants for WEBGL_debug_renderer_info (Fix for Bug 7: Robustness)
            const UNMASKED_VENDOR_WEBGL = 0x9245;
            const UNMASKED_RENDERER_WEBGL = 0x9246;

            const overrideWebGLContext = (proto) => {
                const originalGetParameter = proto.getParameter;
                
                const spoofedGetParameter = function getParameter(parameter) {
                    
                    // Check against hardcoded constants (Fix for Bug 7).
                    if (parameter === UNMASKED_VENDOR_WEBGL) return persona.webGLVendor;
                    if (parameter === UNMASKED_RENDERER_WEBGL) return persona.webGLRenderer;

                    // (Fix for Bug 6: Aggressive Spoofing)
                    // We do NOT override standard gl.VENDOR/gl.RENDERER. They should return
                    // the browser's generic values (e.g. "Google Inc."), not high-entropy hardware data.

                    return originalGetParameter.call(this, parameter);
                };
                
                // Masking getParameter
                maskAsNative(spoofedGetParameter);

                Object.defineProperty(proto, 'getParameter', {
                    value: spoofedGetParameter,
                    configurable: true
                });
            };

            if (window.WebGLRenderingContext) overrideWebGLContext(WebGLRenderingContext.prototype);
            if (window.WebGL2RenderingContext) overrideWebGLContext(WebGL2RenderingContext.prototype);
            
        } catch (error) {}
    }

})();