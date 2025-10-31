/**
 * ToolRegistry - Manages available tools and capabilities
 *
 * This class provides a registry for all available tools that can be
 * called through the ACP protocol, including filesystem, terminal, and
 * cursor-specific tools.
 */

import {
  ToolError,
  type AdapterConfig,
  type Logger,
  type Tool,
  type ToolProvider,
  type ToolCall,
  type ToolResult,
} from '../types';
import { FilesystemToolProvider } from './filesystem';
import { TerminalToolProvider } from './terminal';
import { CursorToolsProvider } from './cursor-tools';

export class ToolRegistry {
  private config: AdapterConfig;
  private logger: Logger;
  private providers = new Map<string, ToolProvider>();
  private tools = new Map<string, Tool>();

  constructor(config: AdapterConfig, logger: Logger) {
    this.config = config;
    this.logger = logger;

    this.logger.debug('ToolRegistry initialized');

    // Initialize built-in tool providers
    this.initializeProviders();
  }

  /**
   * Registers a tool provider
   */
  registerProvider(provider: ToolProvider): void {
    this.logger.debug(`Registering tool provider: ${provider.name}`);

    this.providers.set(provider.name, provider);

    // Register all tools from the provider
    const tools = provider.getTools();
    for (const tool of tools) {
      this.tools.set(tool.name, tool);
      this.logger.debug(`Registered tool: ${tool.name}`);
    }
  }

  /**
   * Unregisters a tool provider
   */
  unregisterProvider(providerName: string): void {
    this.logger.debug(`Unregistering tool provider: ${providerName}`);

    const provider = this.providers.get(providerName);
    if (!provider) {
      this.logger.warn(`Tool provider not found: ${providerName}`);
      return;
    }

    // Unregister all tools from the provider
    const tools = provider.getTools();
    for (const tool of tools) {
      this.tools.delete(tool.name);
      this.logger.debug(`Unregistered tool: ${tool.name}`);
    }

    this.providers.delete(providerName);
  }

  /**
   * Gets all available tools
   */
  getTools(): Tool[] {
    return Array.from(this.tools.values());
  }

  /**
   * Gets a specific tool by name
   */
  getTool(name: string): Tool | undefined {
    return this.tools.get(name);
  }

  /**
   * Gets all registered providers
   */
  getProviders(): ToolProvider[] {
    return Array.from(this.providers.values());
  }

  /**
   * Checks if a tool is available
   */
  hasTool(name: string): boolean {
    return this.tools.has(name);
  }

  /**
   * Executes a tool call
   */
  async executeTool(toolCall: ToolCall): Promise<ToolResult> {
    this.logger.debug(`Executing tool: ${toolCall.name}`, {
      id: toolCall.id,
      parameters: toolCall.parameters,
    });

    const startTime = Date.now();

    try {
      const tool = this.tools.get(toolCall.name);
      if (!tool) {
        const duration = Date.now() - startTime;
        return {
          success: false,
          error: `Tool not found: ${toolCall.name}`,
          metadata: {
            toolName: toolCall.name,
            duration,
            executedAt: new Date(),
          },
        };
      }

      // Validate parameters
      const validationError = this.validateToolParameters(
        tool,
        toolCall.parameters
      );
      if (validationError) {
        const duration = Date.now() - startTime;
        return {
          success: false,
          error: `Invalid parameters for ${toolCall.name}: ${validationError}`,
          metadata: {
            toolName: toolCall.name,
            duration,
            executedAt: new Date(),
          },
        };
      }

      // Execute the tool
      const result = await tool.handler(toolCall.parameters);

      const duration = Date.now() - startTime;
      this.logger.debug(`Tool executed in ${duration}ms: ${toolCall.name}`);

      return {
        ...result,
        metadata: {
          ...result.metadata,
          toolName: toolCall.name,
          duration,
          executedAt: new Date(),
        },
      };
    } catch (error) {
      const duration = Date.now() - startTime;
      this.logger.error(
        `Tool execution failed after ${duration}ms: ${toolCall.name}`,
        error
      );

      const errorMessage =
        error instanceof ToolError
          ? error.message
          : error instanceof Error
            ? error.message
            : String(error);

      return {
        success: false,
        error: errorMessage,
        metadata: {
          toolName: toolCall.name,
          duration,
          executedAt: new Date(),
        },
      };
    }
  }

  /**
   * Gets tool capabilities for ACP initialization
   */
  getCapabilities(): Record<string, any> {
    const capabilities: Record<string, any> = {
      tools: Array.from(this.tools.keys()),
      providers: Array.from(this.providers.keys()),
    };

    // Add capability flags based on available tools
    capabilities['filesystem'] =
      this.hasTool('read_file') || this.hasTool('write_file');
    capabilities['terminal'] =
      this.hasTool('execute_command') || this.hasTool('start_shell_session');
    capabilities['cursor'] =
      this.hasTool('search_codebase') || this.hasTool('analyze_code');

    return capabilities;
  }

  /**
   * Gets metrics about tool usage
   */
  getMetrics(): Record<string, any> {
    return {
      totalTools: this.tools.size,
      totalProviders: this.providers.size,
      enabledProviders: Array.from(this.providers.keys()),
      // TODO: Add execution metrics, usage stats, etc.
    };
  }

  /**
   * Validates tool configuration
   */
  validateConfiguration(): string[] {
    const errors: string[] = [];

    // Check if filesystem tools are configured correctly
    if (this.config.tools.filesystem.enabled) {
      if (!this.hasTool('read_file') || !this.hasTool('write_file')) {
        errors.push('Filesystem tools enabled but not properly registered');
      }

      if (this.config.tools.filesystem.allowedPaths.length === 0) {
        errors.push('Filesystem tools enabled but no allowed paths configured');
      }
    }

    // Check if terminal tools are configured correctly
    if (this.config.tools.terminal.enabled) {
      if (!this.hasTool('execute_command')) {
        errors.push('Terminal tools enabled but not properly registered');
      }

      if (this.config.tools.terminal.maxProcesses <= 0) {
        errors.push('Terminal tools enabled but maxProcesses is invalid');
      }
    }

    // Check if cursor tools are configured correctly
    if (this.config.tools.cursor?.enabled !== false) {
      if (!this.hasTool('search_codebase') && !this.hasTool('analyze_code')) {
        errors.push('Cursor tools enabled but not properly registered');
      }
    }

    return errors;
  }

  /**
   * Reloads tool providers based on configuration
   */
  async reload(): Promise<void> {
    this.logger.info('Reloading tool registry');

    try {
      // Clear existing providers and tools
      this.providers.clear();
      this.tools.clear();

      // Re-initialize providers
      this.initializeProviders();

      this.logger.info('Tool registry reloaded successfully');
    } catch (error) {
      this.logger.error('Failed to reload tool registry', error);
      throw new ToolError(
        `Failed to reload tools: ${error instanceof Error ? error.message : String(error)}`,
        undefined,
        error instanceof Error ? error : undefined
      );
    }
  }

  // Private helper methods

  private initializeProviders(): void {
    this.logger.debug('Initializing built-in tool providers');

    // Initialize filesystem tools
    if (this.config.tools.filesystem.enabled) {
      const filesystemProvider = new FilesystemToolProvider(
        this.config,
        this.logger
      );
      this.registerProvider(filesystemProvider);
    }

    // Initialize terminal tools
    if (this.config.tools.terminal.enabled) {
      const terminalProvider = new TerminalToolProvider(
        this.config,
        this.logger
      );
      this.registerProvider(terminalProvider);
    }

    // Initialize cursor-specific tools
    if (this.config.tools.cursor?.enabled !== false) {
      const cursorProvider = new CursorToolsProvider(this.config, this.logger);
      this.registerProvider(cursorProvider);
    }
  }

  private validateToolParameters(
    tool: Tool,
    parameters: Record<string, any>
  ): string | null {
    // Handle null/undefined parameters
    if (parameters === null || parameters === undefined) {
      return 'Parameters are required and must be an object';
    }

    if (typeof parameters !== 'object' || Array.isArray(parameters)) {
      return 'Parameters must be an object';
    }

    // Basic parameter validation
    const required = tool.parameters.required || [];

    for (const param of required) {
      if (!(param in parameters)) {
        return `Missing required parameter: ${param}`;
      }
      // Check if the parameter value is null or undefined
      if (parameters[param] === null || parameters[param] === undefined) {
        return `Parameter ${param} cannot be null or undefined`;
      }
    }

    // TODO: Implement more sophisticated parameter validation
    // - Type checking
    // - Format validation
    // - Range checking
    // - Custom validators

    return null;
  }
}
