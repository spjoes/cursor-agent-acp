/**
 * InitializationHandler - Handles ACP initialization protocol
 *
 * This class implements the ACP initialization method, which is the first
 * method called by ACP clients to establish communication and discover
 * server capabilities.
 */

import {
  ProtocolError,
  type AdapterConfig,
  type Logger,
  type InitializeParams,
  type InitializeResult,
  type ServerCapabilities,
  type ConnectivityTestResult,
} from '../types';

export class InitializationHandler {
  private config: AdapterConfig;
  private logger: Logger;

  constructor(config: AdapterConfig, logger: Logger) {
    this.config = config;
    this.logger = logger;
  }

  /**
   * Handles the ACP initialize method
   */
  async initialize(params: InitializeParams): Promise<InitializeResult> {
    this.logger.info('Initializing ACP adapter', {
      protocolVersion: params.protocolVersion,
      clientInfo: params.clientInfo,
    });

    try {
      // Validate protocol version
      this.validateProtocolVersion(params.protocolVersion);

      // Log client information if provided
      if (params.clientInfo) {
        this.logger.info('Client information', {
          name: params.clientInfo.name,
          version: params.clientInfo.version,
        });
      }

      // Test cursor-agent connectivity and authentication
      const connectivityTest = await this.testCursorConnectivity();
      if (!connectivityTest.success) {
        throw new ProtocolError(
          `Cursor CLI not available: ${connectivityTest.error}`
        );
      }

      if (!connectivityTest.authenticated) {
        throw new ProtocolError(
          `Cursor authentication required: ${connectivityTest.error || 'Not authenticated'}`
        );
      }

      // Build capabilities based on configuration
      const capabilities = this.buildCapabilities();

      // Echo back the client's protocol version in the same format they sent
      // Accept both string and number formats for maximum compatibility
      const result: InitializeResult = {
        protocolVersion: params.protocolVersion as any, // Echo back exactly as received
        serverInfo: {
          name: 'cursor-agent-acp',
          version: '0.1.0', // Match our package version
        },
        capabilities,
      };

      this.logger.info(`Initialization response: ${JSON.stringify(result)}`);

      this.logger.info('ACP adapter initialized successfully');
      return result;
    } catch (error) {
      this.logger.error('Initialization failed', error);
      throw error instanceof ProtocolError
        ? error
        : new ProtocolError(
            `Initialization failed: ${error instanceof Error ? error.message : String(error)}`,
            error instanceof Error ? error : undefined
          );
    }
  }

  /**
   * Validates the protocol version
   */
  private validateProtocolVersion(
    protocolVersion: string | number | null | undefined
  ): void {
    // Log what we received for debugging
    this.logger.info(
      `Received protocol version: ${JSON.stringify(protocolVersion)} (type: ${typeof protocolVersion})`
    );

    if (!protocolVersion && protocolVersion !== 0) {
      throw new ProtocolError('Protocol version is required');
    }

    // Accept both string and number protocol versions
    if (
      typeof protocolVersion !== 'string' &&
      typeof protocolVersion !== 'number'
    ) {
      throw new ProtocolError('Protocol version must be a string or number');
    }

    // Check for extremely long versions (potential DoS) - only for strings
    if (typeof protocolVersion === 'string' && protocolVersion.length > 100) {
      throw new ProtocolError('Protocol version too long');
    }

    // Validate protocol version is supported
    // Protocol versions are integers (0, 1, etc.) per ACP spec
    // Legacy versions used semver strings and are treated as version 0
    if (typeof protocolVersion === 'number') {
      // Accept versions 0 and 1 (current ACP protocol versions)
      if (protocolVersion !== 0 && protocolVersion !== 1) {
        throw new ProtocolError(
          `Unsupported protocol version: ${protocolVersion}. Only versions 0 and 1 are supported.`
        );
      }
    } else {
      // String versions are legacy and treated as version 0
      // Accept any string that starts with "0" (e.g., "0.1.0", "0.x.x")
      const versionStr = String(protocolVersion);
      const majorVersion = versionStr.split('.')[0];
      if (majorVersion !== '0') {
        throw new ProtocolError(
          `Unsupported protocol version: ${protocolVersion}. Only version 0.x.x (legacy) is supported for string versions.`
        );
      }
    }

    this.logger.info(`Using ACP protocol version: ${protocolVersion}`);
  }

  /**
   * Builds server capabilities based on configuration
   */
  private buildCapabilities(): ServerCapabilities {
    return {
      sessionManagement: true,
      streaming: true,
      toolCalling: true,
      fileSystem: this.config.tools.filesystem.enabled,
      terminal: this.config.tools.terminal.enabled,
      contentTypes: ['text', 'code', 'image'],
    };
  }

  /**
   * Tests cursor-agent connectivity and authentication
   */
  private async testCursorConnectivity(): Promise<ConnectivityTestResult> {
    try {
      // TODO: Replace with actual cursor-agent connectivity test
      // For now, return a mock successful result
      this.logger.debug('Testing cursor-agent connectivity...');

      // Simulate connectivity test
      await new Promise((resolve) => setTimeout(resolve, 10));

      return {
        success: true,
        version: '1.0.0',
        authenticated: true,
      };
    } catch (error) {
      this.logger.error('Cursor connectivity test failed', error);
      return {
        success: false,
        error: error instanceof Error ? error.message : String(error),
      };
    }
  }
}
