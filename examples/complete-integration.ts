#!/usr/bin/env tsx

/**
 * Complete Integration Example - Phase 3
 *
 * This example demonstrates the complete integration of all Phase 2 and Phase 3 components:
 * - Full ACP protocol implementation
 * - Real Cursor CLI integration
 * - Session management with persistence
 * - Content processing (text, code, image)
 * - Tool execution (filesystem, terminal)
 * - HTTP and stdio transport
 *
 * Run with: npx tsx examples/complete-integration.ts
 */

import { CursorAgentAdapter } from '../src/adapter/cursor-agent-adapter';
import { createLogger } from '../src/utils/logger';
import type { AdapterConfig, AcpRequest, ContentBlock } from '../src/types';

// Configuration for the example
const exampleConfig: AdapterConfig = {
  logLevel: 'info',
  sessionDir: './tmp/example-sessions',
  maxSessions: 50,
  sessionTimeout: 1800000, // 30 minutes
  tools: {
    filesystem: {
      enabled: true,
      allowedPaths: ['./', './tmp/', '/tmp/'],
    },
    terminal: {
      enabled: true,
      maxProcesses: 10,
    },
  },
  cursor: {
    timeout: 60000,
    retries: 2,
  },
};

// Example logger
const logger = createLogger({ level: 'info' });

async function demonstrateCompleteIntegration() {
  logger.info('🚀 Starting Cursor Agent ACP Adapter - Complete Integration Example');

  // Create and initialize adapter
  const adapter = new CursorAgentAdapter(exampleConfig, { logger });

  try {
    // Initialize all components
    logger.info('📋 Initializing adapter components...');
    await adapter.initialize();

    // Show adapter status
    const status = adapter.getStatus();
    logger.info('✅ Adapter initialized successfully', {
      components: status.components,
      toolCount: status.metrics.tools?.totalTools || 0,
    });

    // Demonstrate ACP Protocol Flow
    await demonstrateAcpProtocol(adapter);

    // Demonstrate Session Management
    await demonstrateSessionManagement(adapter);

    // Demonstrate Content Processing
    await demonstrateContentProcessing(adapter);

    // Demonstrate Tool Execution
    await demonstrateToolExecution(adapter);

    // Demonstrate Cursor CLI Integration
    await demonstrateCursorIntegration(adapter);

    // Start HTTP server for external clients
    logger.info('🌐 Starting HTTP server on port 3000...');
    await adapter.startHttpServer(3000);

    logger.info('🎉 Complete integration demonstration successful!');
    logger.info('💡 HTTP server running on http://localhost:3000');
    logger.info('💡 Send ACP requests via POST to test the adapter');

    // Keep server running for a while
    setTimeout(async () => {
      logger.info('⏰ Shutting down after demonstration...');
      await adapter.shutdown();
      process.exit(0);
    }, 30000); // Run for 30 seconds

  } catch (error) {
    logger.error('❌ Integration example failed', error);
    await adapter.shutdown();
    process.exit(1);
  }
}

async function demonstrateAcpProtocol(adapter: CursorAgentAdapter) {
  logger.info('🔧 Testing ACP Protocol Methods...');

  // Test initialization
  const initRequest: AcpRequest = {
    jsonrpc: '2.0',
    method: 'initialize',
    id: 'example-init',
    params: {
      protocolVersion: '0.1.0',
      clientInfo: {
        name: 'ExampleClient',
        version: '1.0.0',
      },
    },
  };

  const initResponse = await adapter.processRequest(initRequest);
  logger.info('✅ Initialize:', {
    success: !initResponse.error,
    capabilities: initResponse.result?.capabilities,
  });

  // Test tools list
  const toolsRequest: AcpRequest = {
    jsonrpc: '2.0',
    method: 'tools/list',
    id: 'example-tools',
  };

  const toolsResponse = await adapter.processRequest(toolsRequest);
  logger.info('✅ Tools available:', {
    count: toolsResponse.result?.tools?.length || 0,
    tools: toolsResponse.result?.tools?.map((t: any) => t.name) || [],
  });
}

async function demonstrateSessionManagement(adapter: CursorAgentAdapter) {
  logger.info('📝 Testing Session Management...');

  // Create session
  const createRequest: AcpRequest = {
    jsonrpc: '2.0',
    method: 'session/new',
    id: 'example-create',
    params: {
      metadata: {
        name: 'Example Integration Session',
        description: 'Demonstrating complete ACP adapter functionality',
        tags: ['example', 'integration', 'demo'],
        projectPath: './',
      },
    },
  };

  const createResponse = await adapter.processRequest(createRequest);
  const sessionId = createResponse.result?.sessionId;

  logger.info('✅ Session created:', { sessionId });

  // List sessions
  const listRequest: AcpRequest = {
    jsonrpc: '2.0',
    method: 'session/list',
    id: 'example-list',
    params: { limit: 10 },
  };

  const listResponse = await adapter.processRequest(listRequest);
  logger.info('✅ Sessions listed:', {
    count: listResponse.result?.sessions?.length || 0,
  });

  return sessionId;
}

async function demonstrateContentProcessing(adapter: CursorAgentAdapter) {
  logger.info('📄 Testing Content Processing...');

  // Create a session for content testing
  const sessionRequest: AcpRequest = {
    jsonrpc: '2.0',
    method: 'session/new',
    id: 'content-session',
    params: {
      metadata: { name: 'Content Processing Demo' },
    },
  };

  const sessionResponse = await adapter.processRequest(sessionRequest);
  const sessionId = sessionResponse.result?.sessionId;

  // Test mixed content processing
  const mixedContent: ContentBlock[] = [
    {
      type: 'text',
      text: 'Here is a TypeScript example for a React component:',
    },
    {
      type: 'code',
      language: 'typescript',
      code: `import React from 'react';

interface Props {
  name: string;
  age: number;
}

const UserCard: React.FC<Props> = ({ name, age }) => {
  return (
    <div className="user-card">
      <h2>{name}</h2>
      <p>Age: {age}</p>
    </div>
  );
};

export default UserCard;`,
      filename: 'UserCard.tsx',
    },
    {
      type: 'text',
      text: 'Please review this code and suggest improvements.',
    },
  ];

  const promptRequest: AcpRequest = {
    jsonrpc: '2.0',
    method: 'session/prompt',
    id: 'content-prompt',
    params: {
      sessionId,
      content: mixedContent,
      stream: false,
      metadata: { source: 'example', type: 'code_review' },
    },
  };

  const promptResponse = await adapter.processRequest(promptRequest);

  logger.info('✅ Content processed:', {
    success: !promptResponse.error,
    messageId: promptResponse.result?.messageId,
    responseBlocks: promptResponse.result?.content?.length || 0,
  });

  // Test streaming content
  const streamRequest: AcpRequest = {
    jsonrpc: '2.0',
    method: 'session/prompt',
    id: 'stream-prompt',
    params: {
      sessionId,
      content: [
        {
          type: 'text',
          text: 'Explain the benefits of TypeScript in modern web development.',
        },
      ],
      stream: true,
    },
  };

  const streamResponse = await adapter.processRequest(streamRequest);
  logger.info('✅ Streaming content:', {
    success: !streamResponse.error,
    messageId: streamResponse.result?.messageId,
  });
}

async function demonstrateToolExecution(adapter: CursorAgentAdapter) {
  logger.info('🛠️ Testing Tool Execution...');

  // Test filesystem tools
  const writeRequest: AcpRequest = {
    jsonrpc: '2.0',
    method: 'tools/call',
    id: 'write-file',
    params: {
      name: 'write_file',
      parameters: {
        path: './tmp/example-output.txt',
        content: `Cursor Agent ACP Adapter - Integration Example
Generated at: ${new Date().toISOString()}

This file demonstrates the filesystem tool integration.

Features tested:
- File system operations (read/write)
- Terminal command execution
- Session management
- Content processing
- ACP protocol compliance

All systems operational! 🎉`,
      },
    },
  };

  const writeResponse = await adapter.processRequest(writeRequest);
  logger.info('✅ File write:', {
    success: writeResponse.result?.success,
    path: writeResponse.result?.result?.path,
  });

  // Test reading the file back
  const readRequest: AcpRequest = {
    jsonrpc: '2.0',
    method: 'tools/call',
    id: 'read-file',
    params: {
      name: 'read_file',
      parameters: {
        path: './tmp/example-output.txt',
      },
    },
  };

  const readResponse = await adapter.processRequest(readRequest);
  logger.info('✅ File read:', {
    success: readResponse.result?.success,
    size: readResponse.result?.result?.size,
  });

  // Test terminal command
  const cmdRequest: AcpRequest = {
    jsonrpc: '2.0',
    method: 'tools/call',
    id: 'run-command',
    params: {
      name: 'execute_command',
      parameters: {
        command: 'ls',
        args: ['-la', './tmp/'],
      },
    },
  };

  const cmdResponse = await adapter.processRequest(cmdRequest);
  logger.info('✅ Command execution:', {
    success: cmdResponse.result?.success,
    exitCode: cmdResponse.result?.result?.exitCode,
  });

  // Test directory listing
  const listDirRequest: AcpRequest = {
    jsonrpc: '2.0',
    method: 'tools/call',
    id: 'list-dir',
    params: {
      name: 'list_directory',
      parameters: {
        path: './tmp/',
        show_hidden: false,
      },
    },
  };

  const listDirResponse = await adapter.processRequest(listDirRequest);
  logger.info('✅ Directory listing:', {
    success: listDirResponse.result?.success,
    entries: listDirResponse.result?.result?.entries?.length || 0,
  });
}

async function demonstrateCursorIntegration(adapter: CursorAgentAdapter) {
  logger.info('🎯 Testing Cursor CLI Integration...');

  // Create session for Cursor integration
  const sessionRequest: AcpRequest = {
    jsonrpc: '2.0',
    method: 'session/new',
    id: 'cursor-session',
    params: {
      metadata: {
        name: 'Cursor Integration Demo',
        description: 'Testing real cursor-agent CLI integration'
      },
    },
  };

  const sessionResponse = await adapter.processRequest(sessionRequest);
  const sessionId = sessionResponse.result?.sessionId;

  // Send a prompt that will be processed by Cursor CLI
  const cursorPromptRequest: AcpRequest = {
    jsonrpc: '2.0',
    method: 'session/prompt',
    id: 'cursor-prompt',
    params: {
      sessionId,
      content: [
        {
          type: 'text',
          text: 'What are the key principles of clean code?',
        },
      ],
      stream: false,
      metadata: {
        source: 'cursor_integration_demo',
        model: 'gpt-4',
      },
    },
  };

  try {
    const cursorResponse = await adapter.processRequest(cursorPromptRequest);

    logger.info('✅ Cursor CLI integration:', {
      success: !cursorResponse.error,
      messageId: cursorResponse.result?.messageId,
      responseContentBlocks: cursorResponse.result?.content?.length || 0,
    });

    if (cursorResponse.result?.content) {
      logger.info('📝 Sample response preview:', {
        firstBlockType: cursorResponse.result.content[0]?.type,
        firstBlockPreview: cursorResponse.result.content[0]?.text?.substring(0, 100) + '...',
      });
    }

  } catch (error) {
    logger.warn('⚠️ Cursor CLI integration test failed (may need authentication):', {
      error: error instanceof Error ? error.message : String(error),
    });
  }
}

// Example of how to send requests via HTTP
function logHttpExamples() {
  logger.info('📡 HTTP Request Examples:');

  const initExample = {
    jsonrpc: '2.0',
    method: 'initialize',
    id: 'http-init',
    params: {
      protocolVersion: '0.1.0',
      clientInfo: { name: 'HTTPClient', version: '1.0.0' },
    },
  };

  const sessionExample = {
    jsonrpc: '2.0',
    method: 'session/new',
    id: 'http-session',
    params: {
      metadata: { name: 'HTTP Test Session' },
    },
  };

  logger.info('💡 Initialize via HTTP:');
  logger.info(`curl -X POST http://localhost:3000 \\
  -H "Content-Type: application/json" \\
  -d '${JSON.stringify(initExample, null, 2)}'`);

  logger.info('💡 Create session via HTTP:');
  logger.info(`curl -X POST http://localhost:3000 \\
  -H "Content-Type: application/json" \\
  -d '${JSON.stringify(sessionExample, null, 2)}'`);
}

// Handle process signals for graceful shutdown
process.on('SIGINT', () => {
  logger.info('👋 Received SIGINT, shutting down gracefully...');
  process.exit(0);
});

process.on('SIGTERM', () => {
  logger.info('👋 Received SIGTERM, shutting down gracefully...');
  process.exit(0);
});

// Create tmp directory if it doesn't exist
import { mkdirSync } from 'fs';
try {
  mkdirSync('./tmp', { recursive: true });
} catch (error) {
  // Directory might already exist
}

// Run the complete integration demonstration
if (require.main === module) {
  demonstrateCompleteIntegration().catch((error) => {
    logger.error('💥 Complete integration example failed:', error);
    process.exit(1);
  });
}

// Also log HTTP examples for reference
setTimeout(() => {
  logHttpExamples();
}, 5000); // Show examples after 5 seconds

export { demonstrateCompleteIntegration };
