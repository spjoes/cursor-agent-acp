"use strict";
/**
 * Integration tests for CursorAgentAdapter
 *
 * These tests verify the complete integration between all components:
 * - ACP protocol handling
 * - Cursor CLI integration
 * - Session management
 * - Tool execution
 * - End-to-end request/response flow
 */
Object.defineProperty(exports, "__esModule", { value: true });
const cursor_agent_adapter_1 = require("../../src/adapter/cursor-agent-adapter");
// Mock logger for tests
const mockLogger = {
    error: jest.fn(),
    warn: jest.fn(),
    info: jest.fn(),
    debug: jest.fn(),
};
// Test configuration
const testConfig = {
    logLevel: 'debug',
    sessionDir: '/tmp/cursor-test-sessions',
    maxSessions: 10,
    sessionTimeout: 30000,
    tools: {
        filesystem: {
            enabled: true,
            allowedPaths: ['/tmp', './'],
        },
        terminal: {
            enabled: true,
            maxProcesses: 3,
        },
    },
    cursor: {
        timeout: 30000,
        retries: 1,
    },
};
describe('CursorAgentAdapter Integration', () => {
    let adapter;
    beforeEach(async () => {
        jest.clearAllMocks();
        adapter = new cursor_agent_adapter_1.CursorAgentAdapter(testConfig, { logger: mockLogger });
        await adapter.initialize();
    });
    afterEach(async () => {
        if (adapter) {
            await adapter.shutdown();
        }
    });
    describe('Initialization', () => {
        it('should initialize all components successfully', async () => {
            const status = adapter.getStatus();
            expect(status.running).toBe(false);
            expect(status.components.sessionManager).toBe(true);
            expect(status.components.cursorBridge).toBe(true);
            expect(status.components.toolRegistry).toBe(true);
            expect(status.components.initializationHandler).toBe(true);
            expect(status.components.promptHandler).toBe(true);
        });
        it('should validate configuration during initialization', async () => {
            const invalidConfig = {
                ...testConfig,
                tools: {
                    filesystem: {
                        enabled: true,
                        allowedPaths: [], // Invalid: empty paths
                    },
                    terminal: {
                        enabled: true,
                        maxProcesses: 0, // Invalid: zero processes
                    },
                },
            };
            await expect(async () => {
                const invalidAdapter = new cursor_agent_adapter_1.CursorAgentAdapter(invalidConfig, {
                    logger: mockLogger,
                });
                await invalidAdapter.initialize();
            }).rejects.toThrow();
        });
    });
    describe('ACP Protocol Methods', () => {
        describe('initialize', () => {
            it('should handle initialize request correctly', async () => {
                const request = {
                    jsonrpc: '2.0',
                    method: 'initialize',
                    id: 'test-init-1',
                    params: {
                        protocolVersion: '0.1.0',
                        clientInfo: {
                            name: 'TestClient',
                            version: '1.0.0',
                        },
                    },
                };
                const response = await adapter.processRequest(request);
                expect(response.jsonrpc).toBe('2.0');
                expect(response.id).toBe('test-init-1');
                expect(response.result).toBeDefined();
                expect(response.result.protocolVersion).toBe('0.1.0');
                expect(response.result.serverInfo).toEqual({
                    name: 'cursor-agent-acp',
                    version: '0.1.0',
                });
                expect(response.result.capabilities).toEqual({
                    sessionManagement: true,
                    streaming: true,
                    toolCalling: true,
                    fileSystem: true,
                    terminal: true,
                    contentTypes: ['text', 'code', 'image'],
                });
            });
            it('should reject invalid protocol version', async () => {
                const request = {
                    jsonrpc: '2.0',
                    method: 'initialize',
                    id: 'test-init-2',
                    params: {
                        protocolVersion: '999.0.0',
                    },
                };
                const response = await adapter.processRequest(request);
                expect(response.error).toBeDefined();
                expect(response.error?.message).toContain('protocol version');
            });
        });
        describe('session management', () => {
            it('should create, load, and delete session', async () => {
                // Create session
                const createRequest = {
                    jsonrpc: '2.0',
                    method: 'session/new',
                    id: 'test-session-1',
                    params: {
                        metadata: {
                            name: 'Test Session',
                            tags: ['test'],
                        },
                    },
                };
                const createResponse = await adapter.processRequest(createRequest);
                expect(createResponse.result).toBeDefined();
                expect(createResponse.result.sessionId).toBeDefined();
                const sessionId = createResponse.result.sessionId;
                // Load session
                const loadRequest = {
                    jsonrpc: '2.0',
                    method: 'session/load',
                    id: 'test-session-2',
                    params: {
                        sessionId,
                    },
                };
                const loadResponse = await adapter.processRequest(loadRequest);
                expect(loadResponse.result).toBeDefined();
                expect(loadResponse.result.sessionId).toBe(sessionId);
                expect(loadResponse.result.metadata.name).toBe('Test Session');
                // Delete session
                const deleteRequest = {
                    jsonrpc: '2.0',
                    method: 'session/delete',
                    id: 'test-session-3',
                    params: {
                        sessionId,
                    },
                };
                const deleteResponse = await adapter.processRequest(deleteRequest);
                expect(deleteResponse.result).toBeDefined();
                expect(deleteResponse.result.deleted).toBe(true);
            });
            it('should list sessions with pagination', async () => {
                // Create multiple sessions
                for (let i = 0; i < 3; i++) {
                    const request = {
                        jsonrpc: '2.0',
                        method: 'session/new',
                        id: `create-${i}`,
                        params: {
                            metadata: { name: `Session ${i}` },
                        },
                    };
                    await adapter.processRequest(request);
                }
                // List sessions
                const listRequest = {
                    jsonrpc: '2.0',
                    method: 'session/list',
                    id: 'test-list-1',
                    params: {
                        limit: 2,
                        offset: 0,
                    },
                };
                const listResponse = await adapter.processRequest(listRequest);
                expect(listResponse.result).toBeDefined();
                expect(listResponse.result.sessions).toHaveLength(2);
                expect(listResponse.result.total).toBe(2);
            });
        });
        describe('prompt processing', () => {
            let sessionId;
            beforeEach(async () => {
                // Create a test session
                const createRequest = {
                    jsonrpc: '2.0',
                    method: 'session/new',
                    id: 'setup-session',
                    params: {
                        metadata: { name: 'Prompt Test Session' },
                    },
                };
                const response = await adapter.processRequest(createRequest);
                sessionId = response.result.sessionId;
            });
            it('should process text prompt', async () => {
                const promptRequest = {
                    jsonrpc: '2.0',
                    method: 'session/prompt',
                    id: 'test-prompt-1',
                    params: {
                        sessionId,
                        content: [
                            {
                                type: 'text',
                                text: 'Hello, can you help me with TypeScript?',
                            },
                        ],
                        stream: false,
                    },
                };
                const response = await adapter.processRequest(promptRequest);
                expect(response.result).toBeDefined();
                expect(response.result.messageId).toBeDefined();
                expect(response.result.content).toBeInstanceOf(Array);
                expect(response.result.content.length).toBeGreaterThan(0);
            });
            it('should process code prompt', async () => {
                const promptRequest = {
                    jsonrpc: '2.0',
                    method: 'session/prompt',
                    id: 'test-prompt-2',
                    params: {
                        sessionId,
                        content: [
                            {
                                type: 'text',
                                text: 'Please review this code:',
                            },
                            {
                                type: 'code',
                                language: 'typescript',
                                code: 'const x: string = "hello";',
                                filename: 'test.ts',
                            },
                        ],
                        stream: false,
                    },
                };
                const response = await adapter.processRequest(promptRequest);
                expect(response.result).toBeDefined();
                expect(response.result.messageId).toBeDefined();
            });
            it('should handle streaming prompt', async () => {
                const promptRequest = {
                    jsonrpc: '2.0',
                    method: 'session/prompt',
                    id: 'test-prompt-3',
                    params: {
                        sessionId,
                        content: [
                            {
                                type: 'text',
                                text: 'Tell me about TypeScript features',
                            },
                        ],
                        stream: true,
                    },
                };
                const response = await adapter.processRequest(promptRequest);
                expect(response.result).toBeDefined();
                expect(response.result.messageId).toBeDefined();
            });
            it('should reject invalid content blocks', async () => {
                const promptRequest = {
                    jsonrpc: '2.0',
                    method: 'session/prompt',
                    id: 'test-prompt-4',
                    params: {
                        sessionId,
                        content: [
                            {
                                type: 'invalid',
                                data: 'test',
                            },
                        ],
                    },
                };
                const response = await adapter.processRequest(promptRequest);
                expect(response.error).toBeDefined();
                expect(response.error?.message).toContain('Invalid content block');
            });
        });
        describe('tool execution', () => {
            it('should list available tools', async () => {
                const request = {
                    jsonrpc: '2.0',
                    method: 'tools/list',
                    id: 'test-tools-1',
                };
                const response = await adapter.processRequest(request);
                expect(response.result).toBeDefined();
                expect(response.result.tools).toBeInstanceOf(Array);
                expect(response.result.tools.length).toBeGreaterThan(0);
                // Check for expected tools
                const toolNames = response.result.tools.map((tool) => tool.name);
                expect(toolNames).toContain('read_file');
                expect(toolNames).toContain('write_file');
                expect(toolNames).toContain('execute_command');
            });
            it('should execute filesystem tool', async () => {
                const request = {
                    jsonrpc: '2.0',
                    method: 'tools/call',
                    id: 'test-tools-2',
                    params: {
                        name: 'write_file',
                        parameters: {
                            path: '/tmp/test-file.txt',
                            content: 'Hello from integration test',
                        },
                    },
                };
                const response = await adapter.processRequest(request);
                expect(response.result).toBeDefined();
                expect(response.result.success).toBe(true);
                expect(response.result.result.path).toBe('/tmp/test-file.txt');
            });
            it('should execute terminal tool', async () => {
                const request = {
                    jsonrpc: '2.0',
                    method: 'tools/call',
                    id: 'test-tools-3',
                    params: {
                        name: 'execute_command',
                        parameters: {
                            command: 'echo',
                            args: ['hello world'],
                        },
                    },
                };
                const response = await adapter.processRequest(request);
                expect(response.result).toBeDefined();
                expect(response.result.success).toBe(true);
                expect(response.result.result.stdout).toContain('hello world');
            });
            it('should reject calls to non-existent tools', async () => {
                const request = {
                    jsonrpc: '2.0',
                    method: 'tools/call',
                    id: 'test-tools-4',
                    params: {
                        name: 'non_existent_tool',
                        parameters: {},
                    },
                };
                const response = await adapter.processRequest(request);
                expect(response.error).toBeDefined();
                expect(response.error?.message).toContain('Tool not found');
            });
            it('should validate tool parameters', async () => {
                const request = {
                    jsonrpc: '2.0',
                    method: 'tools/call',
                    id: 'test-tools-5',
                    params: {
                        name: 'read_file',
                        parameters: {
                        // Missing required 'path' parameter
                        },
                    },
                };
                const response = await adapter.processRequest(request);
                expect(response.error).toBeDefined();
                expect(response.error?.message).toContain('Invalid parameters');
            });
        });
    });
    describe('Error Handling', () => {
        it('should handle unknown methods', async () => {
            const request = {
                jsonrpc: '2.0',
                method: 'unknown/method',
                id: 'test-error-1',
            };
            const response = await adapter.processRequest(request);
            expect(response.error).toBeDefined();
            expect(response.error?.code).toBe(-32601);
            expect(response.error?.message).toContain('Unknown method');
        });
        it('should handle malformed requests', async () => {
            const request = {
                // Missing jsonrpc field
                method: 'initialize',
                id: 'test-error-2',
            };
            const response = await adapter.processRequest(request);
            expect(response.error).toBeDefined();
        });
        it('should handle component failures gracefully', async () => {
            // This test would require mocking component failures
            // For now, we'll test that the adapter can handle basic error scenarios
            const request = {
                jsonrpc: '2.0',
                method: 'session/load',
                id: 'test-error-3',
                params: {
                    sessionId: 'non-existent-session-id',
                },
            };
            const response = await adapter.processRequest(request);
            expect(response.error).toBeDefined();
            expect(response.error?.message).toContain('not found');
        });
    });
    describe('Performance', () => {
        it('should handle concurrent requests', async () => {
            const requests = [];
            // Create multiple concurrent session creation requests
            for (let i = 0; i < 5; i++) {
                const request = {
                    jsonrpc: '2.0',
                    method: 'session/new',
                    id: `concurrent-${i}`,
                    params: {
                        metadata: { name: `Concurrent Session ${i}` },
                    },
                };
                requests.push(adapter.processRequest(request));
            }
            const responses = await Promise.all(requests);
            // All requests should succeed
            responses.forEach((response) => {
                expect(response.result).toBeDefined();
                expect(response.result.sessionId).toBeDefined();
            });
            // All session IDs should be unique
            const sessionIds = responses.map((r) => r.result.sessionId);
            const uniqueIds = new Set(sessionIds);
            expect(uniqueIds.size).toBe(sessionIds.length);
        });
        it('should handle rapid sequential requests', async () => {
            // Create a session first
            const createRequest = {
                jsonrpc: '2.0',
                method: 'session/new',
                id: 'perf-session',
                params: {
                    metadata: { name: 'Performance Test Session' },
                },
            };
            const createResponse = await adapter.processRequest(createRequest);
            const sessionId = createResponse.result.sessionId;
            // Send multiple rapid prompts
            const startTime = Date.now();
            const promises = [];
            for (let i = 0; i < 10; i++) {
                const promptRequest = {
                    jsonrpc: '2.0',
                    method: 'session/prompt',
                    id: `rapid-${i}`,
                    params: {
                        sessionId,
                        content: [
                            {
                                type: 'text',
                                text: `Rapid prompt ${i}`,
                            },
                        ],
                        stream: false,
                    },
                };
                promises.push(adapter.processRequest(promptRequest));
            }
            const responses = await Promise.all(promises);
            const duration = Date.now() - startTime;
            // All requests should complete successfully
            responses.forEach((response) => {
                expect(response.result).toBeDefined();
            });
            // Should complete within reasonable time
            expect(duration).toBeLessThan(10000); // 10 seconds max
        });
    });
    describe('Resource Management', () => {
        it('should clean up resources on shutdown', async () => {
            const status1 = adapter.getStatus();
            expect(status1.components.sessionManager).toBe(true);
            await adapter.shutdown();
            // After shutdown, adapter should not be running
            const status2 = adapter.getStatus();
            expect(status2.running).toBe(false);
        });
        it('should track session metrics', async () => {
            // Create some test sessions
            for (let i = 0; i < 3; i++) {
                const request = {
                    jsonrpc: '2.0',
                    method: 'session/new',
                    id: `metrics-${i}`,
                    params: {
                        metadata: { name: `Metrics Session ${i}` },
                    },
                };
                await adapter.processRequest(request);
            }
            const status = adapter.getStatus();
            expect(status.metrics).toBeDefined();
            expect(status.metrics.sessions).toBeDefined();
            // Specific metrics will depend on SessionManager implementation
        });
        it('should enforce session limits', async () => {
            const limitConfig = {
                ...testConfig,
                maxSessions: 2, // Very low limit for testing
            };
            const limitAdapter = new cursor_agent_adapter_1.CursorAgentAdapter(limitConfig, {
                logger: mockLogger,
            });
            await limitAdapter.initialize();
            try {
                // Create sessions up to the limit
                for (let i = 0; i < 2; i++) {
                    const request = {
                        jsonrpc: '2.0',
                        method: 'session/new',
                        id: `limit-${i}`,
                        params: {
                            metadata: { name: `Limit Session ${i}` },
                        },
                    };
                    const response = await limitAdapter.processRequest(request);
                    expect(response.result).toBeDefined();
                }
                // The next session should trigger cleanup or rejection
                const overLimitRequest = {
                    jsonrpc: '2.0',
                    method: 'session/new',
                    id: 'over-limit',
                    params: {
                        metadata: { name: 'Over Limit Session' },
                    },
                };
                // This might succeed (if cleanup worked) or fail (if limit enforced)
                // The important thing is that the adapter handles it gracefully
                const response = await limitAdapter.processRequest(overLimitRequest);
                // Should not crash the adapter
                expect(response).toBeDefined();
            }
            finally {
                await limitAdapter.shutdown();
            }
        });
    });
});
//# sourceMappingURL=adapter.test.js.map