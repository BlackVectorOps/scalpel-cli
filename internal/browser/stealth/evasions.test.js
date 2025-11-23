
// internal/browser/stealth/evasions_test.js
// In-browser unit test suite for evasions.js

// Simple in-browser test runner framework
const ScalpelTestRunner = {
    tests: [],
    results: [],
    define(name, testFunc) {
        this.tests.push({ name, testFunc });
    },
    // Run tests sequentially
    async run() {
        this.results = [];
        console.log("Starting Scalpel Stealth Evasion Tests...");
        
        for (const test of this.tests) {
            try {
                // Await guarantees sequential execution.
                await test.testFunc();
                this.results.push({ name: test.name, status: 'PASS' });
            } catch (error) {
                // Capture stack trace for better debugging.
                this.results.push({ name: test.name, status: 'FAIL', error: error.message, stack: error.stack });
                console.error(`FAIL: ${test.name}`, error);
            }
        }

        console.log("Tests finished.");
        // Expose results globally so the environment can retrieve them
        // Check if window exists (for worker compatibility)
        if (typeof window !== 'undefined') {
            window.SCALPEL_TEST_RESULTS = this.results;
        }
    }
};

// Simple assertion library
const assert = {
    equal(actual, expected, message) {
        if (actual !== expected) {
            throw new Error(`${message}: Expected ${expected}, but got ${actual}`);
        }
    },
    notEqual(actual, expected, message) {
        if (actual === expected) {
            throw new Error(`${message}: Expected value not to be ${expected}, but it was.`);
        }
    },
    isTrue(condition, message) {
        if (!condition) {
            throw new Error(message);
        }
    },
    isFalse(condition, message) {
        if (condition) {
            throw new Error(message);
        }
    },
    // Helper for asynchronous exception testing
    async throwsAsync(asyncFunc, expectedErrorType, message) {
        try {
            await asyncFunc();
            throw new Error(`${message}: Expected an exception but none was thrown.`);
        } catch (error) {
            // Handle specific assertion failures or expected validation errors
            if (error.message.includes('Expected an exception but none was thrown') || error.message.includes('Required member is undefined')) {
                if (!(expectedErrorType && error instanceof expectedErrorType)) {
                     throw error;
                }
            }
            
            if (expectedErrorType && !(error instanceof expectedErrorType)) {
                 throw new Error(`${message}: Expected error type ${expectedErrorType.name}, but got ${error.name}. Error: ${error.message}`);
            }
            // Success: An exception of the expected type was thrown.
        }
    },
    // Assertion for native function checks
    isNative(func, message) {
        // The global override implementation in evasions.js handles this correctly.
        const nativeString = Function.prototype.toString.call(func);
        assert.isTrue(nativeString.includes('[native code]'), `${message}: Function ${func.name || ''} does not appear native. Got: ${nativeString}`);
    }
};


// --- Tests Definitions ---

// Check if running in a worker context
const isWorker = typeof window === 'undefined';

ScalpelTestRunner.define('Evasion: Webdriver Flag Removal', () => {
    assert.isFalse(navigator.webdriver, 'navigator.webdriver should be false');
    
    // Test Descriptor Mimicry.
    const navProto = typeof Navigator !== 'undefined' ? Navigator.prototype : Object.getPrototypeOf(navigator);
    const descriptor = Object.getOwnPropertyDescriptor(navProto, 'webdriver');
    assert.isTrue(descriptor !== undefined, 'Webdriver descriptor should exist on prototype/object');
    assert.isTrue(typeof descriptor.get === 'function', 'Webdriver should have a getter');
});

// (Test for Fix 1: Function.prototype.toString.call detection)
ScalpelTestRunner.define('Evasion: Advanced Masking (Global Override)', () => {
    // We test several masked functions to ensure the Global Override is robust.
    const testCases = [
        { name: 'navigator.permissions.query', func: navigator.permissions && navigator.permissions.query },
        { name: 'decodingInfo', func: navigator.mediaCapabilities && navigator.mediaCapabilities.decodingInfo },
        { name: 'getHighEntropyValues', func: navigator.userAgentData && navigator.userAgentData.getHighEntropyValues },
    ];

    if (!isWorker) {
        testCases.push({ name: 'chrome.runtime.connect', func: window.chrome && window.chrome.runtime && window.chrome.runtime.connect });

        // Setup WebGL context for WebGL testing (if available)
        const canvas = document.createElement('canvas');
        const gl = canvas.getContext('webgl') || canvas.getContext('experimental-webgl');
        if (gl) {
            testCases.push({ name: 'getParameter', func: gl.getParameter });
        }
        
        // Add Plugins methods if available
        if (navigator.plugins && navigator.plugins.refresh) {
             testCases.push({ name: 'refresh', func: navigator.plugins.refresh });
        }
    }


    for (const { name, func } of testCases) {
        if (!func) {
            continue;
        }

        const funcName = func.name || name;
        const expectedString = `function ${funcName}() { [native code] }`;

        // 1. Standard check (L1 masking)
        assert.equal(func.toString(), expectedString, `${name}.toString() failed (L1)`);

        // 2. The critical test: Using Function.prototype.toString.call()
        const resultViaCall = Function.prototype.toString.call(func);
        assert.equal(resultViaCall, expectedString, `Function.prototype.toString.call(${name}) detection succeeded (Global override failed)`);
    }

    // 3. L2 Authenticity Check (Test the toString function itself)
    assert.isNative(Function.prototype.toString, 'Function.prototype.toString L2 masking');
});

// NEW TEST CASE: Verify that property getters (like navigator.userAgent) are properly masked.
ScalpelTestRunner.define('Evasion: Property Getter Masking (Descriptor Analysis)', () => {
    const navProto = typeof Navigator !== 'undefined' ? Navigator.prototype : Object.getPrototypeOf(navigator);

    const propertiesToTest = [
        { obj: navProto, prop: 'userAgent' },
        { obj: navProto, prop: 'platform' },
        { obj: navProto, prop: 'webdriver' },
        { obj: navProto, prop: 'hardwareConcurrency' },
    ];

    if (!isWorker) {
        propertiesToTest.push(
            { obj: Screen.prototype, prop: 'width' },
            { obj: Screen.prototype, prop: 'colorDepth' },
            // We check the window instance for these properties as that's where the override occurs
            { obj: window, prop: 'devicePixelRatio', instance: true },
            { obj: window, prop: 'innerWidth', instance: true }
        );
    }

    // Add UserAgentData properties if available (on the instance, not the prototype for the mock)
    if (navigator.userAgentData) {
        propertiesToTest.push({ obj: navigator.userAgentData, prop: 'mobile', instance: true });
        propertiesToTest.push({ obj: navigator.userAgentData, prop: 'platform', instance: true });
    }

    for (const { obj, prop, instance } of propertiesToTest) {
        if (!obj) continue;

        let descriptor;
        
        if (instance) {
            // If checking an instance property
            descriptor = Object.getOwnPropertyDescriptor(obj, prop);
        } else {
            // Otherwise, walk the prototype chain (standard behavior)
            let currentObj = obj;
            while (currentObj && !descriptor) {
                descriptor = Object.getOwnPropertyDescriptor(currentObj, prop);
                if (descriptor) break;
                // Handle potential prototype loops
                const proto = Object.getPrototypeOf(currentObj);
                if (proto === currentObj || proto === null) break;
                currentObj = proto;
            }
        }
        
        // Skip if descriptor not found (e.g. property doesn't exist in this environment or wasn't overridden)
        if (!descriptor) continue;
        
        // Skip if it's a value property instead of an accessor property
        if (!descriptor.get) continue;

        assert.isTrue(typeof descriptor.get === 'function', `Getter for ${prop} should be a function.`);

        // 1. Check the name property of the getter function.
        const getterName = descriptor.get.name;
        assert.equal(getterName, `get ${prop}`, `Getter name for ${prop} is incorrect. Got: ${getterName}`);

        // 2. Check the toString() representation of the getter function.
        const getterString = descriptor.get.toString();
        const expectedString = `function get ${prop}() { [native code] }`;
        assert.equal(getterString, expectedString, `Getter toString() for ${prop} is not masked correctly. Got: ${getterString}`);
        
        // 3. Advanced check: Using Function.prototype.toString.call() on the getter.
        const getterStringViaCall = Function.prototype.toString.call(descriptor.get);
        assert.equal(getterStringViaCall, expectedString, `Function.prototype.toString.call() on getter for ${prop} detection succeeded.`);
    }
});


// Updated: Check modernization fixes (V3, runtime.id)
ScalpelTestRunner.define('Evasion: window.chrome simulation (Modernized)', () => {
    if (isWorker) return;

    assert.isTrue(window.chrome !== undefined, 'window.chrome should be defined');
    assert.isTrue(window.chrome.runtime !== undefined, 'window.chrome.runtime should be defined');
    
    // Check runtime.id (FIX)
    assert.isTrue(window.chrome.runtime.id !== undefined, 'chrome.runtime.id should be defined');
    assert.isTrue(window.chrome.runtime.id.length > 0, 'chrome.runtime.id should not be empty');
    
    // Check getManifest (V3) (FIX)
    if (window.chrome.runtime.getManifest) {
        const manifest = window.chrome.runtime.getManifest();
        assert.equal(manifest.manifest_version, 3, 'Manifest version should be 3 (V3)');
    }

    // Check getURL uses the defined ID
    if (window.chrome.runtime.getURL) {
        const url = window.chrome.runtime.getURL("test.html");
        assert.isTrue(url.includes(window.chrome.runtime.id), 'chrome.runtime.getURL should use the runtime ID');
    }
});

ScalpelTestRunner.define('Persona Application: Navigator Properties', () => {
    const persona = (typeof window !== 'undefined' ? window.SCALPEL_PERSONA : self.SCALPEL_PERSONA) || {};
    
    if (persona.userAgent) {
        assert.equal(navigator.userAgent, persona.userAgent, 'UserAgent mismatch');
        // Check derived properties
        const expectedAppVersion = persona.userAgent.replace(/^Mozilla\//, '');
        assert.equal(navigator.appVersion, expectedAppVersion, 'appVersion mismatch');
    }
    
    if (persona.platform) {
        assert.equal(navigator.platform, persona.platform, 'Platform mismatch');
    }

    if (persona.languages && persona.languages.length > 0) {
        assert.equal(navigator.languages.join(','), persona.languages.join(','), 'Languages mismatch');
        assert.equal(navigator.language, persona.languages[0], 'Primary language mismatch');
        assert.isTrue(Object.isFrozen(navigator.languages), 'Languages array should be frozen');
    }

    const expectedHWConcurrency = persona.hardwareConcurrency || 8;
    assert.equal(navigator.hardwareConcurrency, expectedHWConcurrency, 'hardwareConcurrency mismatch');

    if ('deviceMemory' in navigator) {
        const expectedDevMemory = persona.deviceMemory || 8;
        assert.equal(navigator.deviceMemory, expectedDevMemory, 'deviceMemory mismatch');
    }

    // (MODERNIZATION: pdfViewerEnabled Test)
    const expectedPdfEnabled = persona.pdfViewerEnabled !== false; // Default to true
    assert.equal(navigator.pdfViewerEnabled, expectedPdfEnabled, 'navigator.pdfViewerEnabled mismatch');
});


// (MODERNIZATION: User-Agent Client Hints Test)
ScalpelTestRunner.define('Evasion: User-Agent Client Hints (UA-CH)', async () => {
    const persona = (typeof window !== 'undefined' ? window.SCALPEL_PERSONA : self.SCALPEL_PERSONA) || {};
    if (!navigator.userAgentData || !persona.userAgentData) {
        return;
    }

    const uaData = persona.userAgentData;

    // 1. Check low-entropy values
    assert.equal(navigator.userAgentData.mobile, uaData.mobile || false, 'UA-CH mobile mismatch');
    
    // 2. Check high-entropy values (basic check)
    const hints = ["architecture", "platformVersion"];
    const highEntropy = await navigator.userAgentData.getHighEntropyValues(hints);

    assert.equal(highEntropy.architecture, uaData.architecture, 'UA-CH architecture mismatch');
    assert.equal(highEntropy.platformVersion, uaData.platformVersion, 'UA-CH platformVersion mismatch');

    // 3. Check input validation
    await assert.throwsAsync(() => navigator.userAgentData.getHighEntropyValues("string"), TypeError, 'Calling getHighEntropyValues("string")');

    // 4. Check masking
    const targetFunc = navigator.userAgentData.getHighEntropyValues;
    assert.isNative(targetFunc, 'getHighEntropyValues masking');
});


ScalpelTestRunner.define('Persona Application: Screen and Window Properties', () => {
    if (isWorker) return;

    const persona = window.SCALPEL_PERSONA || {};

    if (!persona.width || !persona.height) return;

    assert.equal(window.screen.width, persona.width, 'Screen width mismatch');
    assert.equal(window.screen.height, persona.height, 'Screen height mismatch');

    // Verify Device Pixel Ratio (DPR).
    if (persona.devicePixelRatio) {
        assert.equal(window.devicePixelRatio, persona.devicePixelRatio, 'Device Pixel Ratio (DPR) mismatch');
    }

    // Verify Window Dimensions
    assert.equal(window.outerWidth, persona.width, 'window.outerWidth mismatch');
    assert.equal(window.innerHeight, persona.height, 'window.innerHeight mismatch');
});

ScalpelTestRunner.define('Evasion: Permissions API (Structure, Functionality)', async () => {
    // Use globalThis to access PermissionStatus/Notification robustly in workers/windows
    if (navigator.permissions && navigator.permissions.query && globalThis.PermissionStatus) {
        
        // 1. Check functionality
        let result;
        try {
             result = await navigator.permissions.query({ name: 'notifications' });
        } catch (e) {
            return; // Skip if permission query fails
        }
        
        // Determine expected state robustly
        let expectedState = 'prompt'; 
        if (globalThis.Notification) {
            expectedState = (Notification.permission === 'default') ? 'prompt' : Notification.permission;
        }

        assert.equal(result.state, expectedState, 'Permissions API notification state mismatch');
        
        // 2. Check input validation
        await assert.throwsAsync(() => navigator.permissions.query(), TypeError, 'Calling query() without arguments');
        
        // Check validation for missing 'name' property (enforced by the updated evasion script)
        await assert.throwsAsync(async () => {
            try {
                await navigator.permissions.query({});
            } catch (error) {
                // Verify the specific error message for required member
                if (error.message.includes('Required member is undefined')) {
                    throw error;
                }
                throw new Error(`Expected specific TypeError for missing 'name', but got: ${error.message}`);
            }
        }, TypeError, 'Calling query({}) without name');
    }
});


// NEW TEST: Validate the determinism and independence of the canvas poisoning (FIX)
ScalpelTestRunner.define('Evasion: Canvas Fingerprinting Defense (Determinism and Independence)', () => {
    if (isWorker) return;

    const canvas = document.createElement('canvas');
    const ctx = canvas.getContext('2d');
    if (!ctx) return;

    // Use a small canvas
    canvas.width = 50;
    canvas.height = 1;

    // 1. Draw known content (e.g., solid gray)
    const initialValue = 128;
    ctx.fillStyle = `rgb(${initialValue}, ${initialValue}, ${initialValue})`;
    ctx.fillRect(0, 0, 50, 1);

    // Helper to get image data
    const getData = () => ctx.getImageData(0, 0, 50, 1).data;

    // 2. Read data (poisoned)
    const data1 = getData();
    
    // 3. Read data again (must be identical for determinism)
    const data2 = getData();

    assert.equal(data1.length, data2.length, 'Canvas data length mismatch between reads');
    
    let determinismDifferences = 0;
    let poisoningOccurred = false;
    let correlatedNoiseCount = 0;
    let pixelCount = 0;

    for (let i = 0; i < data1.length; i+=4) {
        pixelCount++;

        // Check determinism
        if (data1[i] !== data2[i] || data1[i+1] !== data2[i+1] || data1[i+2] !== data2[i+2]) {
            determinismDifferences++;
        }

        // Check if poisoning occurred (values changed from initial)
        if (data1[i] !== initialValue || data1[i+1] !== initialValue || data1[i+2] !== initialValue) {
            poisoningOccurred = true;
        }

        // Check for correlated noise (R, G, B having the exact same noise offset)
        const noiseR = data1[i] - initialValue;
        const noiseG = data1[i+1] - initialValue;
        const noiseB = data1[i+2] - initialValue;

        if (noiseR === noiseG && noiseG === noiseB && noiseR !== 0) {
            // If all channels have the same non-zero noise, it's correlated.
            correlatedNoiseCount++;
        }
    }

    // Assertions
    assert.equal(determinismDifferences, 0, 'Canvas poisoning is not deterministic between reads');
    assert.isTrue(poisoningOccurred, 'Canvas poisoning did not alter pixel data');
    
    // We expect correlated noise to be rare with the improved algorithm (statistically possible but unlikely for many pixels)
    // If more than 50% of pixels show correlation, something is likely wrong (Probability of correlation is low).
    assert.isTrue(correlatedNoiseCount < (pixelCount / 2), `Canvas poisoning shows high correlation between R/G/B channels (Count: ${correlatedNoiseCount}/${pixelCount})`);
});


ScalpelTestRunner.define('Evasion: WebGL Spoofing (Robustness)', () => {
    if (isWorker) return;

    const persona = window.SCALPEL_PERSONA || {};
    if (!persona.webGLVendor || !persona.webGLRenderer) {
        return;
    }

    const canvas = document.createElement('canvas');
    // Try WebGL2 first, then WebGL1
    const gl = canvas.getContext('webgl2') || canvas.getContext('webgl') || canvas.getContext('experimental-webgl');

    if (!gl) {
        return;
    }

    // Standardized constants (fallback)
    const UNMASKED_VENDOR_WEBGL = 0x9245;
    const UNMASKED_RENDERER_WEBGL = 0x9246;

    // 1. Get the extension object (preferred way)
    const debugInfo = gl.getExtension('WEBGL_debug_renderer_info');

    // 2. Verify spoofing
    if (debugInfo) {
        // Test using extension object constants (validates the robust implementation)
        assert.equal(gl.getParameter(debugInfo.UNMASKED_VENDOR_WEBGL), persona.webGLVendor, 'WebGL Vendor mismatch (via extension constant)');
        assert.equal(gl.getParameter(debugInfo.UNMASKED_RENDERER_WEBGL), persona.webGLRenderer, 'WebGL Renderer mismatch (via extension constant)');
    } else {
        // Test using hardcoded constants
        assert.equal(gl.getParameter(UNMASKED_VENDOR_WEBGL), persona.webGLVendor, 'WebGL Vendor mismatch (via hardcoded constant)');
        assert.equal(gl.getParameter(UNMASKED_RENDERER_WEBGL), persona.webGLRenderer, 'WebGL Renderer mismatch (via hardcoded constant)');
    }
});


// (MODERNIZATION: Media Capabilities Test)
ScalpelTestRunner.define('Evasion: Media Capabilities API (decodingInfo)', async () => {
    if (!navigator.mediaCapabilities || !navigator.mediaCapabilities.decodingInfo) {
        return;
    }

    // 1. Test standard H.264 video support
    const h264Config = {
        type: 'file',
        video: {
            contentType: 'video/mp4; codecs="avc1.42E01E"',
            width: 1920, height: 1080, bitrate: 1000000, framerate: 30
        }
    };
    const h264Result = await navigator.mediaCapabilities.decodingInfo(h264Config);
    assert.isTrue(h264Result.supported, 'H.264 video should be supported');
    // Verify configuration is echoed back (FIX check)
    assert.equal(h264Result.configuration.video.contentType, h264Config.video.contentType, 'H.264 configuration echo mismatch');

    // 2. Test input validation
    await assert.throwsAsync(() => navigator.mediaCapabilities.decodingInfo(), TypeError, 'Calling decodingInfo() without arguments');
    await assert.throwsAsync(() => navigator.mediaCapabilities.decodingInfo(null), TypeError, 'Calling decodingInfo(null)');
});


// (MODERNIZATION: Updated tests for the 5 standard plugins)
// Updated: Added descriptor checks (writable/enumerable)
ScalpelTestRunner.define('Evasion: navigator.plugins and mimeTypes (Modern Structure)', () => {
    if (isWorker) return;
    
    // 1. Check existence and basic structure
    assert.equal(navigator.plugins.length, 5, 'navigator.plugins length should be 5');
    assert.equal(navigator.mimeTypes.length, 1, 'navigator.mimeTypes length should be 1');

    const pdfMime = navigator.mimeTypes['application/pdf'];
    const firstPlugin = navigator.plugins[0];

    // 6. Check enumerability (Advanced structural check)
    const pluginKeys = Object.keys(navigator.plugins);
    assert.isTrue(pluginKeys.includes('0'), 'Indexed properties should be enumerable on PluginArray');
    // Crucial check: Named properties must NOT be enumerable on the PluginArray itself.
    assert.isFalse(pluginKeys.includes('Chrome PDF Viewer'), 'Named properties should NOT be enumerable on PluginArray');

    const firstPluginKeys = Object.keys(firstPlugin);
    // Check that indexed MimeTypes (e.g. '0') are NOT enumerable on the Plugin object itself
    assert.isFalse(firstPluginKeys.includes('0'), 'Indexed MimeTypes should NOT be enumerable on Plugin');
    
    // Check that standard properties (name) ARE enumerable on the Plugin object
    assert.isTrue(firstPluginKeys.includes('name'), 'Plugin "name" property should be enumerable');
    
    // 7. Check Getter Masking on Plugin/MimeType properties (Defense in depth)
    const pluginDescriptor = Object.getOwnPropertyDescriptor(firstPlugin, 'name');
    assert.isNative(pluginDescriptor.get, 'Plugin.name getter masking');
    
    const mimeDescriptor = Object.getOwnPropertyDescriptor(pdfMime, 'type');
    assert.isNative(mimeDescriptor.get, 'MimeType.type getter masking');
    
    const enabledPluginDescriptor = Object.getOwnPropertyDescriptor(pdfMime, 'enabledPlugin');
    assert.isNative(enabledPluginDescriptor.get, 'MimeType.enabledPlugin getter masking');

    // 8. Check Descriptor Flags (Read-only checks) - Validates the structural fixes.
    const pluginArrayDescriptor0 = Object.getOwnPropertyDescriptor(navigator.plugins, '0');
    assert.isTrue(pluginArrayDescriptor0 !== undefined, 'PluginArray[0] descriptor should exist');
    assert.isFalse(pluginArrayDescriptor0.writable, 'PluginArray[0] should be read-only (writable: false)'); // FIX CHECK

    const mimeTypeArrayDescriptor0 = Object.getOwnPropertyDescriptor(navigator.mimeTypes, '0');
    assert.isFalse(mimeTypeArrayDescriptor0.writable, 'MimeTypeArray[0] should be read-only (writable: false)'); // FIX CHECK
    
    const pluginLengthDescriptor = Object.getOwnPropertyDescriptor(firstPlugin, 'length');
    assert.isFalse(pluginLengthDescriptor.writable, 'Plugin.length should be read-only (writable: false)'); // FIX CHECK

    const pluginMimeIndexDescriptor = Object.getOwnPropertyDescriptor(firstPlugin, '0');
    assert.isFalse(pluginMimeIndexDescriptor.writable, 'Plugin[0] (MimeType index) should be read-only'); // FIX CHECK
    assert.isFalse(pluginMimeIndexDescriptor.enumerable, 'Plugin[0] (MimeType index) should not be enumerable');

});

// Run the tests automatically when the script loads
if (isWorker) {
    ScalpelTestRunner.run();
} else {
    // Ensure DOM is ready before running tests that interact with it (like Canvas/WebGL)
    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', () => ScalpelTestRunner.run());
    } else {
        // Run slightly delayed if already interactive/complete to allow initialization
        setTimeout(() => ScalpelTestRunner.run(), 50);
    }
}