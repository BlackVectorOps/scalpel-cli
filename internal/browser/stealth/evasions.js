// File: internal/browser/stealth/evasions.js
// This script runs in the browser context before any page scripts (via CDP Page.addScriptToEvaluateOnNewDocument).

(function() {
    'use strict';

    // -- 1. Scope & Configuration --
    // Support both Window (Main Thread) and Worker contexts.
    const globalScope = typeof window !== 'undefined' ? window : self;
    const isWorker = typeof window === 'undefined';

    // Retrieve configuration injected via globalScope.SCALPEL_PERSONA
    const persona = globalScope.SCALPEL_PERSONA || {};

    // -- 2. Utility Functions --

    // Helper to get the authentic Function.prototype.toString (for masking)
    const authenticNativeToString = Function.prototype.toString;

    // Map to store masked functions and their desired string representations.
    const maskedFunctions = new WeakMap();

    // Utility function to make functions appear native.
    // Must be defined before overrideGetter.
    const maskAsNative = (func, nameHint = '') => {
        if (typeof func !== 'function') {
            return func;
        }

        // If the function is already masked, return it.
        if (maskedFunctions.has(func)) {
             return func;
        }

        try {
            const name = nameHint || func.name || '';
            // The format handles both regular functions ("name") and getters ("get name").
            const nativeString = `function ${name}() { [native code] }`;

            const proxy = new Proxy(func, {
                get(target, prop, receiver) {
                    // Ensure 'name' property is correct on the proxy if hinted.
                    if (prop === 'name' && nameHint && nameHint !== target.name) {
                        return nameHint;
                    }
                    return Reflect.get(target, prop, receiver);
                },
                apply(target, thisArg, argumentsList) {
                    return Reflect.apply(target, thisArg, argumentsList);
                }
            });

            maskedFunctions.set(proxy, nativeString);
            maskedFunctions.set(func, nativeString); // Defense in depth

            return proxy;

        } catch (e) {
            return func;
        }
    };

    // Helper for safe property definition (getters).
    const overrideGetter = (obj, prop, value) => {
        try {
            if (!obj) return;
            // Try to find the original descriptor on the object or its prototype chain.
            let originalDescriptor = Object.getOwnPropertyDescriptor(obj, prop);
            if (!originalDescriptor && obj) {
                let proto = Object.getPrototypeOf(obj);
                while (proto && !originalDescriptor) {
                    originalDescriptor = Object.getOwnPropertyDescriptor(proto, prop);
                    proto = Object.getPrototypeOf(proto);
                }
            }

            // Determine flags. We mimic the original if found.
            const configurable = originalDescriptor ? originalDescriptor.configurable : true;
            const enumerable = originalDescriptor ? originalDescriptor.enumerable : true;

            // FIX: Define the getter function using a standard function instead of an arrow function.
            const getterFunc = function() {
                return value;
            };

            // FIX: Mask the getter function to appear native (e.g., "get userAgent").
            const maskedGetter = maskAsNative(getterFunc, `get ${prop}`);

            // Define the new property.
            const newDescriptor = {
                get: maskedGetter, // Use the masked getter
                configurable: configurable,
                enumerable: enumerable,
            };

            Object.defineProperty(obj, prop, newDescriptor);
        } catch (error) {
            // console.warn(`Scalpel Stealth: Failed to override property ${prop}`, error);
        }
    };


    // -- 3. Evasion: Global Function.prototype.toString Override --
    
    const spoofedGlobalToString = function toString() {
        // 'this' context is the function being converted to a string.
        if (this && maskedFunctions.has(this)) {
            return maskedFunctions.get(this);
        }
        return authenticNativeToString.call(this);
    };

    // Mask the override itself.
    try {
        const authenticToStringString = authenticNativeToString.call(authenticNativeToString);
        maskedFunctions.set(spoofedGlobalToString, authenticToStringString);
    } catch (e) {
        maskedFunctions.set(spoofedGlobalToString, "function toString() { [native code] }");
    }
    
    try {
        Object.defineProperty(Function.prototype, 'toString', {
            value: spoofedGlobalToString,
            configurable: true,
            enumerable: false, // Native behavior
            writable: true,
        });
    } catch (e) {}


    // ===========================================================================
    // SHARED EVASIONS (Window & Worker)
    // ===========================================================================

    // -- Evasion: Navigator Properties --
    
    // Use Navigator.prototype if available, otherwise fallback to navigator instance (for older/worker contexts)
    const navProto = typeof Navigator !== 'undefined' ? Navigator.prototype : Object.getPrototypeOf(navigator);


    // 1. WebDriver
    if (navigator.webdriver !== false) {
        overrideGetter(navProto, 'webdriver', false);
    }
    // Fallback check on the instance itself.
    try {
        if (navigator.webdriver === true) {
             overrideGetter(navigator, 'webdriver', false);
        }
    } catch (error) {}

    // 2. Basic UserAgent/Platform
    if (persona.userAgent) {
        overrideGetter(navProto, 'userAgent', persona.userAgent);
        // appVersion is typically "5.0 (Platform; ...)"
        overrideGetter(navProto, 'appVersion', persona.userAgent.replace(/^Mozilla\//, ''));
    }
    if (persona.platform) {
        overrideGetter(navProto, 'platform', persona.platform);
    }
    if (Array.isArray(persona.languages) && persona.languages.length > 0) {
        const languages = Object.freeze([...persona.languages]);
        overrideGetter(navProto, 'languages', languages);
        overrideGetter(navProto, 'language', languages[0]);
    }

    // 3. Hardware Concurrency & Device Memory
    const hwConcurrency = persona.hardwareConcurrency || 8;
    overrideGetter(navProto, 'hardwareConcurrency', hwConcurrency);
    
    const devMemory = persona.deviceMemory || 8;
    if ('deviceMemory' in navigator) {
         overrideGetter(navProto, 'deviceMemory', devMemory);
    }

    // 4. PDF Viewer Enabled (Standard for Desktop)
    if (persona.pdfViewerEnabled !== false) {
        overrideGetter(navProto, 'pdfViewerEnabled', true);
    }

    // -- Evasion: User-Agent Client Hints (UA-CH) --
    if (persona.userAgentData && navigator.userAgentData) {
        try {
            const uaData = persona.userAgentData;

            // Create a mock object inheriting from the native prototype (NavigatorUAData).
            const proto = globalScope.NavigatorUAData ? globalScope.NavigatorUAData.prototype : Object.getPrototypeOf(navigator.userAgentData);
            const mockUAData = Object.create(proto);
            
            const deepCopy = (data) => JSON.parse(JSON.stringify(data));

            // Low entropy properties
            // FIX: Ensure getters on the mock object are also masked.
            Object.defineProperty(mockUAData, 'brands', {
                get: maskAsNative(function() { return deepCopy(uaData.brands || []); }, 'get brands'),
                enumerable: true,
                configurable: true
            });
            Object.defineProperty(mockUAData, 'mobile', {
                get: maskAsNative(function() { return uaData.mobile || false; }, 'get mobile'),
                enumerable: true,
                configurable: true
            });
            Object.defineProperty(mockUAData, 'platform', {
                get: maskAsNative(function() { return uaData.platform || ""; }, 'get platform'),
                enumerable: true,
                configurable: true
            });

            // High entropy values
            const spoofedGetHighEntropyValues = function getHighEntropyValues(hints) {
                if (!hints || !Array.isArray(hints)) {
                     return Promise.reject(new TypeError("Failed to execute 'getHighEntropyValues' on 'NavigatorUAData': The provided value is not of type sequence."));
                }

                const result = {};
                result.brands = deepCopy(uaData.brands || []);
                result.mobile = uaData.mobile || false;
                result.platform = uaData.platform || "";

                hints.forEach(hint => {
                    if (uaData.hasOwnProperty(hint)) {
                        const value = uaData[hint];
                        if (typeof value === 'object' && value !== null) {
                            result[hint] = deepCopy(value);
                        } else {
                            result[hint] = value;
                        }
                    }
                });
                
                return Promise.resolve(result);
            };

            const maskedGetHighEntropyValues = maskAsNative(spoofedGetHighEntropyValues, 'getHighEntropyValues');
            Object.defineProperty(mockUAData, 'getHighEntropyValues', {
                value: maskedGetHighEntropyValues,
                configurable: true,
                enumerable: true,
                writable: true
            });

            // Finally, override the navigator property to point to our mock object.
            overrideGetter(navProto, 'userAgentData', mockUAData);

        } catch (e) {}
    }

    // -- Evasion: Permissions API --
    // This works in both Window and Worker contexts in modern browsers
    try {
        if (navigator.permissions && navigator.permissions.query && globalScope.PermissionStatus) {
            const originalQuery = navigator.permissions.query;
            
            const spoofedQuery = function query(parameters) {
                // Input validation
                if (!parameters) {
                    throw new TypeError("Failed to execute 'query' on 'Permissions': 1 argument required, but only 0 present.");
                }
                if (typeof parameters !== 'object' || parameters === null) {
                     throw new TypeError("Failed to execute 'query' on 'Permissions': The provided value is not of type 'PermissionDescriptor'.");
                }

                // Standard validation for required 'name' property (added for robustness)
                if (!parameters.name) {
                     // Mimic Chrome's specific error message for missing name
                     throw new TypeError("Failed to execute 'query' on 'Permissions': Failed to read the 'name' property from 'PermissionDescriptor': Required member is undefined.");
                }


                if (parameters.name === 'notifications') {
                    // Workers might not have Notification object, default to prompt
                    const permissionState = (globalScope.Notification)
                        ? ((Notification.permission === 'default') ? 'prompt' : Notification.permission)
                        : 'prompt';
                    
                    try {
                        return originalQuery.call(this, parameters).then(result => {
                            // Proxy the result to force the state property
                            return new Proxy(result, {
                                get: function(target, prop, receiver) {
                                    if (prop === 'state') {
                                        return permissionState;
                                    }
                                    return Reflect.get(target, prop, receiver);
                                }
                            });
                        });
                    } catch (error) {
                        throw error;
                    }
                }
                return originalQuery.call(this, parameters);
            };

            const maskedQuery = maskAsNative(spoofedQuery, 'query');

            Object.defineProperty(navigator.permissions, 'query', {
                value: maskedQuery,
                configurable: true,
                writable: true, 
                enumerable: true
            });
        }
    } catch (error) {}


    // ===========================================================================
    // MAIN THREAD ONLY EVASIONS (DOM & Visuals)
    // ===========================================================================
    
    if (!isWorker) {

        // -- Evasion: Deterministic Canvas Poisoning --
        // Use a seeded PRNG to ensure consistency across reloads for the same persona.
        (() => {
             // 1. PRNG: Mulberry32 (Fast, deterministic, 32-bit)
            // Seed derivation: Use persona.seed or fallback to a hash of the userAgent.
            let seedVal = persona.seed || 0;
            if (!seedVal && persona.userAgent) {
                for (let i = 0; i < persona.userAgent.length; i++) {
                    seedVal = ((seedVal << 5) - seedVal) + persona.userAgent.charCodeAt(i);
                    seedVal |= 0; // Convert to 32bit integer
                }
            }
            // If no UA, fallback to a hardcoded default to ensure determinism (never use random)
            if (!seedVal) seedVal = 1337; 

            // Initialize PRNG state (scoped to this closure)
            let state = seedVal;

            // State-advancing PRNG function (used for getImageData)
            const mulberry32 = () => {
                let t = state += 0x6D2B79F5;
                t = Math.imul(t ^ (t >>> 15), t | 1);
                t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
                return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
            };

            // Helper: Generate a deterministic shift based on text content (for measureText)
            // FIX: Ensure this uses the global seed (seedVal), NOT the advancing 'state'.
            const getDrift = (input) => {
                // If input is string, hash it
                let hash = seedVal; // Use the fixed seed
                if (typeof input === 'string') {
                    for(let i = 0; i < input.length; i++) {
                        hash = ((hash << 5) - hash) + input.charCodeAt(i);
                        hash |= 0;
                    }
                } else {
                    // Should ideally only be used for strings in measureText context now.
                    hash = input;
                }
                // Use hash to generate a pseudo-random number deterministically (stateless PRNG based on hash)
                let t = hash + 0x6D2B79F5;
                t = Math.imul(t ^ (t >>> 15), t | 1);
                t ^= t + Math.imul(t ^ (t >>> 7), t | 61); // Added step for better distribution
                return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
            };


            // 2. Spoof: CanvasRenderingContext2D.getImageData (Pixel Poisoning)
            // This defeats "canvas fingerprinting" that reads back pixel data.
            if (window.CanvasRenderingContext2D) {
                const originalGetImageData = CanvasRenderingContext2D.prototype.getImageData;
                
                const spoofedGetImageData = function getImageData(sx, sy, sw, sh) {
                    // FIX: Reset the PRNG state before processing to ensure determinism across calls.
                    state = seedVal;

                    const imageData = originalGetImageData.apply(this, arguments);
                    // Only apply noise if we actually got data
                    if (!imageData || !imageData.data) return imageData;

                    const data = imageData.data;
                    const len = data.length;
                    
                    // Apply salt.
                    // FIX: Generate independent noise for R, G, B channels using the advancing PRNG.
                    for (let i = 0; i < len; i += 4) {
                        // We skip the Alpha channel (i+3) usually to avoid visual artifacts
                        
                        // Generate noise: -1, 0, or 1. (Math.floor(mulberry32() * 3) - 1)
                        
                        // R
                        data[i] = data[i] + (Math.floor((mulberry32() * 3)) - 1);
                        // G
                        data[i+1] = data[i+1] + (Math.floor((mulberry32() * 3)) - 1);
                        // B
                        data[i+2] = data[i+2] + (Math.floor((mulberry32() * 3)) - 1);
                        
                        // Clamp values (Uint8ClampedArray handles this natively)
                    }
                    return imageData;
                };

                Object.defineProperty(CanvasRenderingContext2D.prototype, 'getImageData', {
                    value: maskAsNative(spoofedGetImageData, 'getImageData'),
                    configurable: true, enumerable: true, writable: true
                });
            }

            // 3. Spoof: CanvasRenderingContext2D.measureText (Font Poisoning)
            // This defeats "font fingerprinting" / "text metrics".
            if (window.CanvasRenderingContext2D) {
                const originalMeasureText = CanvasRenderingContext2D.prototype.measureText;
                
                const spoofedMeasureText = function measureText(text) {
                    const metrics = originalMeasureText.apply(this, arguments);
                    
                    // We need to proxy the TextMetrics object to be robust.
                    
                    // Calculate deterministic noise for this specific text string using the stateless getDrift.
                    // Small drift: -0.1 to 0.1 pixels
                    const drift = (getDrift(text) * 0.2) - 0.1; 

                    // Create a proxy to override the width getter
                    return new Proxy(metrics, {
                        get(target, prop, receiver) {
                            if (prop === 'width') {
                                const actual = Reflect.get(target, prop, receiver);
                                return actual + drift;
                            }
                            return Reflect.get(target, prop, receiver);
                        }
                    });
                };
                
                Object.defineProperty(CanvasRenderingContext2D.prototype, 'measureText', {
                    value: maskAsNative(spoofedMeasureText, 'measureText'),
                    configurable: true, enumerable: true, writable: true
                });
            }

        })();

        // -- Evasion: Screen Properties --
        if (persona.width && persona.height && window.screen) {
            const screenProto = Screen.prototype;
            overrideGetter(screenProto, 'width', persona.width);
            overrideGetter(screenProto, 'height', persona.height);
            
            const availWidth = persona.availWidth || persona.width;
            const availHeight = persona.availHeight || persona.height;
            const colorDepth = persona.colorDepth || 24;

            overrideGetter(screenProto, 'availWidth', availWidth);
            overrideGetter(screenProto, 'availHeight', availHeight);
            overrideGetter(screenProto, 'colorDepth', colorDepth);
            overrideGetter(screenProto, 'pixelDepth', colorDepth);
            
            // Window dimensions (on 'window' instance)
            try {
                const props = ['outerWidth', 'outerHeight', 'innerWidth', 'innerHeight'];
                props.forEach(p => {
                    const val = (p.toLowerCase().includes('width')) ? persona.width : persona.height;
                    // Only override if strictly necessary to avoid suspicion
                    if (window[p] !== val) {
                        // FIX: Use overrideGetter for window properties to ensure masking.
                        overrideGetter(window, p, val);
                    }
                });
            } catch (e) {}

            if (persona.devicePixelRatio) {
                try {
                    // FIX: Use overrideGetter for DPR.
                    overrideGetter(window, 'devicePixelRatio', persona.devicePixelRatio);
                } catch (e) {}
            }
        }

        // -- Evasion: window.chrome (Robust) --

        if (window.chrome === undefined) {
            Object.defineProperty(window, 'chrome', {
                value: {},
                writable: true,
                configurable: true
            });
        }

        const ensureProperty = (obj, prop, defaultValue = {}) => {
            if (!obj) return;
            if (obj[prop] === undefined) {
                try {
                    Object.defineProperty(obj, prop, {
                        value: defaultValue,
                        writable: true,
                        configurable: true,
                        enumerable: true
                    });
                } catch (e) {}
            }
        };

        // Helper to create Event mocks (addListener, etc)
        const createEventMock = (name) => {
            const mockEvent = {
                addListener: function addListener() {},
                removeListener: function removeListener() {},
                hasListener: function hasListener() { return false; },
                hasListeners: function hasListeners() { return false; },
                dispatch: function dispatch() {} 
            };
            Object.keys(mockEvent).forEach(key => {
                const func = mockEvent[key];
                Object.defineProperty(mockEvent, key, { 
                    value: maskAsNative(func, key), 
                    writable: true, 
                    enumerable: true, 
                    configurable: true 
                });
            });
            return mockEvent;
        };

        ensureProperty(window.chrome, 'runtime', {});
        ensureProperty(window.chrome, 'app', { isInstalled: false });
        ensureProperty(window.chrome, 'webstore', {});

        const runtime = window.chrome.runtime;
        
        // Helper to patch functions safely
        const patchObjectFunc = (obj, name, mockImplementation) => {
            if (!obj) return;
            const originalFunc = typeof obj[name] === 'function' ? obj[name] : mockImplementation;
            const maskedFunc = maskAsNative(originalFunc, name);
            try {
                 if (obj[name] !== maskedFunc) {
                    Object.defineProperty(obj, name, {
                        value: maskedFunc,
                        writable: true,
                        configurable: true,
                        enumerable: true
                    });
                }
            } catch (e) {}
        };

        // 1. Patch chrome.runtime
        if (runtime) {
            if (!runtime.onConnect) runtime.onConnect = createEventMock('onConnect');
            if (!runtime.onMessage) runtime.onMessage = createEventMock('onMessage');
            if (!runtime.onInstalled) runtime.onInstalled = createEventMock('onInstalled');
            if (!runtime.onStartup) runtime.onStartup = createEventMock('onStartup');

            patchObjectFunc(runtime, 'connect', function connect() { return { disconnect: () => {} }; });
            patchObjectFunc(runtime, 'sendMessage', function sendMessage() {});
            
            // FIX: Modernize to Manifest V3 simulation.
            patchObjectFunc(runtime, 'getManifest', function getManifest() { 
                return { 
                    manifest_version: 3,
                    name: "Scalpel Simulation",
                    version: "1.0.0"
                }; 
            });
            
            // Use a realistic example ID
            const extensionId = "pfjfdincgonkdjnmjgmckfofkghjgcdd";

            patchObjectFunc(runtime, 'getURL', function getURL(path) { return `chrome-extension://${extensionId}/` + (path || ""); });

            // FIX: Add runtime.id, which is frequently checked.
            if (runtime.id === undefined) {
                // Define as a non-writable property, similar to native behavior.
                 Object.defineProperty(runtime, 'id', {
                    value: extensionId,
                    writable: false,
                    configurable: true,
                    enumerable: true
                });
            }
        }

        // 2. Patch chrome.app (Detailed Simulation)
        if (window.chrome.app) {
            patchObjectFunc(window.chrome.app, 'getDetails', function getDetails() { return null; });
            patchObjectFunc(window.chrome.app, 'getIsInstalled', function getIsInstalled() { return false; });
            patchObjectFunc(window.chrome.app, 'runningState', function runningState() { return "cannot_run"; });
            
            patchObjectFunc(window.chrome.app, 'installState', function installState(callback) { 
                if (typeof callback === 'function') callback('not_installed'); 
            });

            // Add missing Enums
            if (!window.chrome.app.InstallState) {
                Object.defineProperty(window.chrome.app, 'InstallState', {
                    value: { DISABLED: "disabled", INSTALLED: "installed", NOT_INSTALLED: "not_installed" },
                    writable: false, configurable: false, enumerable: true
                });
            }
            if (!window.chrome.app.RunningState) {
                Object.defineProperty(window.chrome.app, 'RunningState', {
                    value: { CANNOT_RUN: "cannot_run", READY_TO_RUN: "ready_to_run", RUNNING: "running" },
                    writable: false, configurable: false, enumerable: true
                });
            }
        }

        // 3. Patch chrome.webstore (Detailed Simulation)
        if (window.chrome.webstore) {
            patchObjectFunc(window.chrome.webstore, 'install', function install(url, onSuccess, onFailure) {
                const errorCallback = onFailure || (typeof onSuccess === 'function' ? undefined : onSuccess);
                if (typeof errorCallback === 'function') {
                    // Mimic async failure
                    setTimeout(() => errorCallback("User cancelled"), 0);
                }
            });
            
            if (!window.chrome.webstore.onInstallStageChanged) {
                window.chrome.webstore.onInstallStageChanged = createEventMock('onInstallStageChanged');
            }
            if (!window.chrome.webstore.onDownloadProgress) {
                window.chrome.webstore.onDownloadProgress = createEventMock('onDownloadProgress');
            }
        }


        // -- Evasion: WebGL --
        if (persona.webGLVendor && persona.webGLRenderer) {
            try {
                // Hardcoded constants (fallback)
                const UNMASKED_VENDOR_WEBGL = 0x9245;
                const UNMASKED_RENDERER_WEBGL = 0x9246;

                const overrideWebGLContext = (proto) => {
                    const originalGetParameter = proto.getParameter;
                    // Need original getExtension to robustly check constants
                    const originalGetExtension = proto.getExtension;

                    const spoofedGetParameter = function getParameter(parameter) {
                        // Check hardcoded constants first
                        if (parameter === UNMASKED_VENDOR_WEBGL) return persona.webGLVendor;
                        if (parameter === UNMASKED_RENDERER_WEBGL) return persona.webGLRenderer;

                        // Robustness: Check constants defined on the extension object itself
                        try {
                            // Use the authentic getExtension method
                            const debugInfo = originalGetExtension.call(this, 'WEBGL_debug_renderer_info');
                            if (debugInfo) {
                                if (parameter === debugInfo.UNMASKED_VENDOR_WEBGL) {
                                    return persona.webGLVendor;
                                }
                                if (parameter === debugInfo.UNMASKED_RENDERER_WEBGL) {
                                    return persona.webGLRenderer;
                                }
                            }
                        } catch (e) {}

                        return originalGetParameter.call(this, parameter);
                    };
                    
                    const maskedGetParameter = maskAsNative(spoofedGetParameter, 'getParameter');
                    Object.defineProperty(proto, 'getParameter', {
                        value: maskedGetParameter,
                        configurable: true,
                        writable: true
                    });
                };

                if (window.WebGLRenderingContext) overrideWebGLContext(WebGLRenderingContext.prototype);
                if (window.WebGL2RenderingContext) overrideWebGLContext(WebGL2RenderingContext.prototype);
            } catch (error) {}
        }

        // -- Evasion: Media Capabilities --
        if (navigator.mediaCapabilities && navigator.mediaCapabilities.decodingInfo) {
            try {
                const nativeDecodingInfo = navigator.mediaCapabilities.decodingInfo;
                const standardCodecs = ['avc1.', 'mp4a.', 'vp09.', 'av01.'];

                const spoofedDecodingInfo = function decodingInfo(configuration) {
                    if (arguments.length === 0) {
                        throw new TypeError("Failed to execute 'decodingInfo' on 'MediaCapabilities': 1 argument required, but only 0 present.");
                    }
                    
                    // Basic validation (added for robustness)
                    if (!configuration || typeof configuration !== 'object') {
                         throw new TypeError("Failed to execute 'decodingInfo' on 'MediaCapabilities': The provided value is not of type 'MediaDecodingConfiguration'.");
                    }


                    let codecString = null;
                    if (configuration.video && configuration.video.contentType) {
                        codecString = configuration.video.contentType;
                    } else if (configuration.audio && configuration.audio.contentType) {
                        codecString = configuration.audio.contentType;
                    }

                    if (codecString && standardCodecs.some(codec => codecString.includes(codec))) {
                        // Return standard "supported" response, echoing back the configuration (as per spec).
                        return Promise.resolve({ 
                            supported: true, 
                            smooth: true, 
                            powerEfficient: true,
                            configuration: configuration 
                        });
                    }

                    return Reflect.apply(nativeDecodingInfo, this, arguments);
                };

                const maskedDecodingInfo = maskAsNative(spoofedDecodingInfo, 'decodingInfo');
                Object.defineProperty(navigator.mediaCapabilities, 'decodingInfo', {
                    value: maskedDecodingInfo,
                    configurable: true,
                    writable: true
                });
            } catch (e) {}
        }

        // -- Evasion: WebRTC (IP Leak Prevention) --
        // Mitigates WebRTC leakage by injecting fake candidates and stripping local IP host candidates.
        (() => {
            if (typeof window === 'undefined' || !window.RTCPeerConnection) return;

            const originalRTC = window.RTCPeerConnection;
            const FAKE_CANDIDATE = "candidate:842163049 1 udp 1677729535 203.0.113.88 58923 typ srflx raddr 0.0.0.0 rport 0";

            // Function to sanitize and inject candidates
            const handleIceCandidateEvent = (event, callback) => {
                if (event.candidate) {
                    const c = event.candidate.candidate;
                    // Prevent Leak: Drop "host" (local IP) candidates
                    if (c.indexOf("typ host") !== -1) return;
                }

                // Injection: Pass a fake event to the callback
                const fakeEvent = {
                    candidate: {
                        candidate: FAKE_CANDIDATE,
                        sdpMid: event.candidate ? event.candidate.sdpMid : "0",
                        sdpMLineIndex: event.candidate ? event.candidate.sdpMLineIndex : 0
                    }
                };
                // Typically we want to control exactly what they see, so we just pass the fake one.
                callback(fakeEvent);
            };

            const spoofedRTC = function RTCPeerConnection(...args) {
                const pc = new originalRTC(...args);

                // 1. Intercept .onicecandidate assignment
                // We use defineProperty to catch when the user sets this property
                let rawOnIceCandidate = null;
                Object.defineProperty(pc, 'onicecandidate', {
                    get: () => rawOnIceCandidate,
                    set: (cb) => {
                        rawOnIceCandidate = cb;
                    },
                    configurable: true,
                    enumerable: true
                });

                // Hook the native event via addEventListener, and manually invoke the user's .onicecandidate property.
                pc.addEventListener('icecandidate', (e) => {
                    const userHandler = pc.onicecandidate; // Access via getter
                    if (typeof userHandler === 'function') {
                        handleIceCandidateEvent(e, userHandler);
                    }
                });

                // 2. Intercept .addEventListener('icecandidate', ...)
                const originalAddEventListener = pc.addEventListener;
                const spoofedAddEventListener = function addEventListener(type, listener, options) {
                    if (type === 'icecandidate' && typeof listener === 'function') {
                        const wrappedListener = (event) => {
                            handleIceCandidateEvent(event, listener);
                        };
                        return originalAddEventListener.call(this, type, wrappedListener, options);
                    }
                    return originalAddEventListener.call(this, type, listener, options);
                };
                
                // Apply mask to instance method override
                pc.addEventListener = maskAsNative(spoofedAddEventListener, 'addEventListener');

                // 3. Spoof SDP injection
                const originalSetLocalDescription = pc.setLocalDescription;
                const spoofedSetLocalDescription = function setLocalDescription(description) {
                    if (description && description.sdp) {
                        description.sdp += "a=" + FAKE_CANDIDATE + "\r\n";
                    }
                    return originalSetLocalDescription.apply(this, arguments);
                };

                // Apply mask to instance method override
                pc.setLocalDescription = maskAsNative(spoofedSetLocalDescription, 'setLocalDescription');

                return pc;
            };

            // Mask the constructor itself
            maskAsNative(spoofedRTC, 'RTCPeerConnection');

            // Override the global constructor
            window.RTCPeerConnection = spoofedRTC;
            // Ensure prototype chain remains valid for instance checks
            window.RTCPeerConnection.prototype = originalRTC.prototype;

        })();


        // -- Evasion: Plugins and MimeTypes --
        // (Based on the advanced Proxy approach, applying to window context only)
        const mockPluginsAndMimeTypes = () => {
            try {
                const mocks = [
                    { plugin: { name: "PDF Viewer", filename: "internal-pdf-viewer", description: "Portable Document Format" } },
                    { plugin: { name: "Chrome PDF Viewer", filename: "internal-pdf-viewer", description: "Portable Document Format" } },
                    { plugin: { name: "Chromium PDF Viewer", filename: "internal-pdf-viewer", description: "Portable Document Format" } },
                    { plugin: { name: "Microsoft Edge PDF Viewer", filename: "internal-pdf-viewer", description: "Portable Document Format" } },
                    { plugin: { name: "WebKit built-in PDF", filename: "internal-pdf-viewer", description: "Portable Document Format" } }
                ];

                const getProto = (protoName) => window[protoName] ? window[protoName].prototype : Object.prototype;
                
                const createMockItem = (protoName, data) => {
                    const obj = Object.create(getProto(protoName));
                    Object.keys(data).forEach(key => {
                        try {
                            // FIX: Define properties as masked getters to mimic native behavior.
                            const getter = maskAsNative(function() { return data[key]; }, `get ${key}`);
                            Object.defineProperty(obj, key, {
                                get: getter,
                                enumerable: true,
                                configurable: true
                            });
                        } catch(e) {}
                    });
                    return obj;
                };

                const internalPlugins = new Map();
                const internalMimeTypes = new Map();
                const pluginList = [];
                const mimeTypeList = [];

                const pdfMimeTypeData = { type: "application/pdf", suffixes: "pdf", description: "Portable Document Format" };
                const sharedPdfMimeType = createMockItem('MimeType', pdfMimeTypeData);
                internalMimeTypes.set(sharedPdfMimeType.type, sharedPdfMimeType);
                mimeTypeList.push(sharedPdfMimeType);

                mocks.forEach(mock => {
                    const plugin = createMockItem('Plugin', mock.plugin);
                    
                    // FIX: Ensure indexed MimeType access on Plugin is read-only and non-enumerable.
                    Object.defineProperty(plugin, 0, {
                        value: sharedPdfMimeType,
                        enumerable: false,
                        configurable: true,
                        writable: false // Native behavior
                    });

                    // FIX: Ensure Plugin.length is read-only.
                    Object.defineProperty(plugin, 'length', { 
                        value: 1, 
                        configurable: true, 
                        writable: false // Native behavior
                    });

                    if (!internalPlugins.has(plugin.name)) {
                        internalPlugins.set(plugin.name, plugin);
                        pluginList.push(plugin);
                    }
                });

                if (pluginList.length > 0) {
                    try {
                        // FIX: Mask the enabledPlugin getter.
                        const enabledPluginGetter = maskAsNative(function() { return pluginList[0]; }, 'get enabledPlugin');
                        Object.defineProperty(sharedPdfMimeType, 'enabledPlugin', {
                            get: enabledPluginGetter,
                            enumerable: true,
                            configurable: true
                        });
                    } catch(e) {}
                }

                const createMockArrayProxy = (protoName, internalMap, items) => {
                    const base = Object.create(getProto(protoName));
                    
                    const mockItem = function item(index) { return items[Number(index)] || null; };
                    Object.defineProperty(base, 'item', { value: maskAsNative(mockItem, 'item'), configurable: true, enumerable: true, writable: true });

                    const mockNamedItem = function namedItem(name) { return internalMap.get(String(name)) || null; };
                    Object.defineProperty(base, 'namedItem', { value: maskAsNative(mockNamedItem, 'namedItem'), configurable: true, enumerable: true, writable: true });

                    if (protoName === 'PluginArray') {
                        const mockRefresh = function refresh() {};
                        Object.defineProperty(base, 'refresh', { value: maskAsNative(mockRefresh, 'refresh'), configurable: true, enumerable: true, writable: true });
                    }
                    
                    // Length on the base object (can be writable, proxy handles access)
                    Object.defineProperty(base, 'length', { value: items.length, configurable: true, writable: true });

                    return new Proxy(base, {
                        get(target, prop, receiver) {
                            if (typeof prop === 'symbol') return Reflect.get(target, prop, receiver);
                            const propString = String(prop);
                            
                            // Handle 'length' specifically for the Proxy view
                            if (propString === 'length') {
                                 return items.length;
                            }

                            const index = Number(propString);
                            
                            if (Number.isInteger(index) && index >= 0 && String(index) === propString) return items[index];
                            if (internalMap.has(propString)) return internalMap.get(propString);
                            return Reflect.get(target, prop, receiver);
                        },
                        getOwnPropertyDescriptor(target, prop) {
                            if (typeof prop === 'symbol') return Reflect.getOwnPropertyDescriptor(target, prop);
                            const propString = String(prop);
                            const index = Number(propString);
                            if (Number.isInteger(index) && index >= 0 && String(index) === propString && index < items.length) {
                                // FIX: Native indexed properties on PluginArray/MimeTypeArray are read-only.
                                return { value: items[index], enumerable: true, configurable: true, writable: false };
                            }
                            // Named properties are NOT enumerable
                            if (internalMap.has(propString)) return undefined;
                            return Reflect.getOwnPropertyDescriptor(target, prop);
                        },
                        ownKeys(target) {
                            const keys = [];
                            for (let i = 0; i < items.length; i++) keys.push(String(i));
                            const baseKeys = Reflect.ownKeys(target);
                            baseKeys.forEach(key => {
                                if (!keys.includes(key) && !(typeof key === 'string' && keys.includes(key))) keys.push(key);
                            });
                            return keys;
                        },
                        has(target, prop) {
                            if (typeof prop === 'symbol') return Reflect.has(target, prop);
                            if (internalMap.has(String(prop))) return true;
                            return Reflect.has(target, prop);
                        }
                    });
                };

                const pluginArray = createMockArrayProxy('PluginArray', internalPlugins, pluginList);
                const mimeTypeArray = createMockArrayProxy('MimeTypeArray', internalMimeTypes, mimeTypeList);

                overrideGetter(navProto, 'plugins', pluginArray);
                overrideGetter(navProto, 'mimeTypes', mimeTypeArray);

            } catch (error) {}
        };

        mockPluginsAndMimeTypes();
    }

})();