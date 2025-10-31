"use strict";
// Global test setup for unit tests
var __createBinding = (this && this.__createBinding) || (Object.create ? (function(o, m, k, k2) {
    if (k2 === undefined) k2 = k;
    var desc = Object.getOwnPropertyDescriptor(m, k);
    if (!desc || ("get" in desc ? !m.__esModule : desc.writable || desc.configurable)) {
      desc = { enumerable: true, get: function() { return m[k]; } };
    }
    Object.defineProperty(o, k2, desc);
}) : (function(o, m, k, k2) {
    if (k2 === undefined) k2 = k;
    o[k2] = m[k];
}));
var __setModuleDefault = (this && this.__setModuleDefault) || (Object.create ? (function(o, v) {
    Object.defineProperty(o, "default", { enumerable: true, value: v });
}) : function(o, v) {
    o["default"] = v;
});
var __importStar = (this && this.__importStar) || (function () {
    var ownKeys = function(o) {
        ownKeys = Object.getOwnPropertyNames || function (o) {
            var ar = [];
            for (var k in o) if (Object.prototype.hasOwnProperty.call(o, k)) ar[ar.length] = k;
            return ar;
        };
        return ownKeys(o);
    };
    return function (mod) {
        if (mod && mod.__esModule) return mod;
        var result = {};
        if (mod != null) for (var k = ownKeys(mod), i = 0; i < k.length; i++) if (k[i] !== "default") __createBinding(result, mod, k[i]);
        __setModuleDefault(result, mod);
        return result;
    };
})();
Object.defineProperty(exports, "__esModule", { value: true });
exports.TEST_CONSTANTS = exports.testHelpers = void 0;
const globals_1 = require("@jest/globals");
// Extend Jest timeout for all tests
globals_1.jest.setTimeout(10000);
// Mock console methods to reduce noise during tests unless explicitly needed
const originalConsole = global.console;
global.console = {
    ...originalConsole,
    log: globals_1.jest.fn(),
    info: globals_1.jest.fn(),
    warn: globals_1.jest.fn(),
    error: globals_1.jest.fn(),
    debug: globals_1.jest.fn(),
};
// Mock process.exit to prevent tests from actually exiting
const mockExit = globals_1.jest.fn();
Object.defineProperty(process, 'exit', {
    value: mockExit,
    writable: true,
});
// Mock environment variables for consistent testing
process.env.NODE_ENV = 'test';
process.env.CURSOR_AGENT_ACP_LOG_LEVEL = 'error';
process.env.CURSOR_AGENT_ACP_SESSION_DIR = '/tmp/test-sessions';
// Custom Jest matchers
expect.extend({
    toBeValidSessionId(received) {
        const isValid = typeof received === 'string' &&
            received.length > 0 &&
            /^[a-zA-Z0-9-_]+$/.test(received);
        if (isValid) {
            return {
                message: () => `expected ${received} not to be a valid session ID`,
                pass: true,
            };
        }
        return {
            message: () => `expected ${received} to be a valid session ID`,
            pass: false,
        };
    },
    toBeValidAcpResponse(received) {
        // For initialization responses and other direct results (not JSON-RPC wrapped)
        const isDirectResult = received &&
            typeof received === 'object' &&
            (received.protocolVersion !== undefined ||
                received.capabilities !== undefined ||
                received.sessionId !== undefined);
        // For JSON-RPC wrapped responses
        const isJsonRpcResponse = received &&
            typeof received === 'object' &&
            typeof received.jsonrpc === 'string' &&
            received.jsonrpc === '2.0' &&
            (received.result !== undefined || received.error !== undefined) &&
            (typeof received.id === 'string' ||
                typeof received.id === 'number' ||
                received.id === null);
        const isValid = isDirectResult || isJsonRpcResponse;
        if (isValid) {
            return {
                message: () => `expected response not to be valid ACP response`,
                pass: true,
            };
        }
        return {
            message: () => `expected response to be valid ACP response, got: ${JSON.stringify(received)}`,
            pass: false,
        };
    },
    toBeValidInitializeResult(received) {
        const isValid = received &&
            typeof received === 'object' &&
            typeof received.protocolVersion === 'string' &&
            received.serverInfo &&
            typeof received.serverInfo.name === 'string' &&
            typeof received.serverInfo.version === 'string' &&
            received.capabilities &&
            typeof received.capabilities === 'object';
        if (isValid) {
            return {
                message: () => `expected response not to be valid initialize result`,
                pass: true,
            };
        }
        return {
            message: () => `expected response to be valid initialize result, got: ${JSON.stringify(received)}`,
            pass: false,
        };
    },
});
// Clean up after each test
afterEach(() => {
    // Clear all mocks
    globals_1.jest.clearAllMocks();
    // Clear console mocks
    global.console.log.mockClear();
    global.console.info.mockClear();
    global.console.warn.mockClear();
    global.console.error.mockClear();
    global.console.debug.mockClear();
    // Reset process.exit mock
    mockExit.mockClear();
});
// Global cleanup
afterAll(() => {
    // Restore original console
    global.console = originalConsole;
});
// Helper functions for tests
exports.testHelpers = {
    // Create a mock ACP request
    createMockAcpRequest: (method, params = {}, id = 1) => ({
        jsonrpc: '2.0',
        method,
        params,
        id,
    }),
    // Create a mock ACP response
    createMockAcpResponse: (result, id = 1) => ({
        jsonrpc: '2.0',
        result,
        id,
    }),
    // Create a mock ACP error response
    createMockAcpError: (code, message, id = 1) => ({
        jsonrpc: '2.0',
        error: { code, message },
        id,
    }),
    // Generate a test session ID
    generateTestSessionId: () => {
        return `test-session-${Date.now()}-${Math.random().toString(36).substr(2, 9)}`;
    },
    // Wait for a specified amount of time
    wait: (ms) => {
        return new Promise((resolve) => setTimeout(resolve, ms));
    },
    // Create a temporary directory for testing
    createTempDir: async () => {
        const fs = await Promise.resolve().then(() => __importStar(require('fs/promises')));
        const path = await Promise.resolve().then(() => __importStar(require('path')));
        const os = await Promise.resolve().then(() => __importStar(require('os')));
        const tempDir = path.join(os.tmpdir(), `cursor-acp-test-${Date.now()}`);
        await fs.mkdir(tempDir, { recursive: true });
        return tempDir;
    },
    // Clean up temporary directory
    cleanupTempDir: async (dirPath) => {
        const fs = await Promise.resolve().then(() => __importStar(require('fs/promises')));
        try {
            await fs.rm(dirPath, { recursive: true, force: true });
        }
        catch (error) {
            // Ignore errors during cleanup
        }
    },
};
// Export commonly used test constants
exports.TEST_CONSTANTS = {
    DEFAULT_TIMEOUT: 5000,
    LONG_TIMEOUT: 10000,
    SHORT_TIMEOUT: 1000,
    MOCK_SESSION_ID: 'mock-session-12345',
    MOCK_USER_ID: 'test-user',
    SAMPLE_TEXT_CONTENT: 'This is sample text content for testing',
    SAMPLE_CODE_CONTENT: 'console.log("Hello, world!");',
    ACP_PROTOCOL_VERSION: '0.1.0',
};
//# sourceMappingURL=setup.js.map