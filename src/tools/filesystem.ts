/**
 * Filesystem Tool Provider
 *
 * Provides secure file system operations for the ACP adapter.
 * Includes path validation and permission checks to prevent unauthorized access.
 */

import { promises as fs } from 'fs';
import * as path from 'path';
import {
  ToolError,
  type AdapterConfig,
  type Logger,
  type Tool,
  type ToolProvider,
  type ToolResult,
} from '../types';

export interface FileSystemConfig {
  enabled: boolean;
  allowedPaths: string[];
  maxFileSize?: number; // in bytes
  allowedExtensions?: string[];
  forbiddenPaths?: string[];
}

export class FilesystemToolProvider implements ToolProvider {
  readonly name = 'filesystem';
  readonly description =
    'File system operations (read, write, list, create directories)';

  // private config: AdapterConfig; // Not needed for current implementation
  private logger: Logger;
  private fsConfig: FileSystemConfig;

  constructor(config: AdapterConfig, logger: Logger) {
    // this.config = config; // Not needed for current implementation
    this.logger = logger;
    this.fsConfig = config.tools.filesystem;

    this.logger.debug('FilesystemToolProvider initialized', {
      enabled: this.fsConfig.enabled,
      allowedPaths: this.fsConfig.allowedPaths,
    });
  }

  getTools(): Tool[] {
    if (!this.fsConfig.enabled) {
      this.logger.debug('Filesystem tools disabled by configuration');
      return [];
    }

    return [
      {
        name: 'read_file',
        description: 'Read the contents of a file',
        parameters: {
          type: 'object',
          properties: {
            path: {
              type: 'string',
              description: 'Path to the file to read',
            },
          },
          required: ['path'],
        },
        handler: this.readFile.bind(this),
      },
      {
        name: 'write_file',
        description: 'Write content to a file',
        parameters: {
          type: 'object',
          properties: {
            path: {
              type: 'string',
              description: 'Path to the file to write',
            },
            content: {
              type: 'string',
              description: 'Content to write to the file',
            },
            encoding: {
              type: 'string',
              description: 'Text encoding (default: utf8)',
              enum: ['utf8', 'ascii', 'base64', 'binary'],
            },
          },
          required: ['path', 'content'],
        },
        handler: this.writeFile.bind(this),
      },
      {
        name: 'list_directory',
        description: 'List the contents of a directory',
        parameters: {
          type: 'object',
          properties: {
            path: {
              type: 'string',
              description: 'Path to the directory to list',
            },
            recursive: {
              type: 'boolean',
              description: 'Whether to list recursively (default: false)',
            },
            show_hidden: {
              type: 'boolean',
              description: 'Whether to show hidden files (default: false)',
            },
          },
          required: ['path'],
        },
        handler: this.listDirectory.bind(this),
      },
      {
        name: 'create_directory',
        description: 'Create a new directory',
        parameters: {
          type: 'object',
          properties: {
            path: {
              type: 'string',
              description: 'Path to the directory to create',
            },
            recursive: {
              type: 'boolean',
              description:
                'Whether to create parent directories (default: true)',
            },
          },
          required: ['path'],
        },
        handler: this.createDirectory.bind(this),
      },
      {
        name: 'delete_file',
        description: 'Delete a file or directory',
        parameters: {
          type: 'object',
          properties: {
            path: {
              type: 'string',
              description: 'Path to the file or directory to delete',
            },
            recursive: {
              type: 'boolean',
              description:
                'Whether to delete directories recursively (default: false)',
            },
          },
          required: ['path'],
        },
        handler: this.deleteFile.bind(this),
      },
      {
        name: 'get_file_info',
        description: 'Get information about a file or directory',
        parameters: {
          type: 'object',
          properties: {
            path: {
              type: 'string',
              description: 'Path to the file or directory',
            },
          },
          required: ['path'],
        },
        handler: this.getFileInfo.bind(this),
      },
    ];
  }

  /**
   * Read file contents
   */
  private async readFile(params: Record<string, any>): Promise<ToolResult> {
    try {
      const filePath = this.validateAndResolvePath(params['path']);

      this.logger.debug('Reading file', { path: filePath });

      // Check file size
      const stats = await fs.stat(filePath);
      const maxSize = this.fsConfig.maxFileSize || 10 * 1024 * 1024; // 10MB default

      if (stats.size > maxSize) {
        throw new ToolError(
          `File too large: ${stats.size} bytes (max: ${maxSize} bytes)`,
          'read_file'
        );
      }

      // Check if it's a file
      if (!stats.isFile()) {
        throw new ToolError(`Path is not a file: ${filePath}`, 'read_file');
      }

      const content = await fs.readFile(filePath, 'utf8');

      return {
        success: true,
        result: {
          content,
          size: stats.size,
          encoding: 'utf8',
          path: filePath,
        },
        metadata: {
          fileSize: stats.size,
          lastModified: stats.mtime.toISOString(),
        },
      };
    } catch (error) {
      this.logger.error('Failed to read file', { error, path: params['path'] });
      return {
        success: false,
        error: error instanceof Error ? error.message : String(error),
      };
    }
  }

  /**
   * Write file contents
   */
  private async writeFile(params: Record<string, any>): Promise<ToolResult> {
    try {
      const filePath = this.validateAndResolvePath(params['path']);
      const content = params['content'];
      const encoding = params['encoding'] || 'utf8';

      this.logger.debug('Writing file', {
        path: filePath,
        contentLength: content.length,
        encoding,
      });

      // Ensure directory exists
      const dir = path.dirname(filePath);
      await fs.mkdir(dir, { recursive: true });

      // Write file
      await fs.writeFile(filePath, content, encoding as 'utf8' | 'utf-8');

      // Get file stats
      const stats = await fs.stat(filePath);

      return {
        success: true,
        result: {
          path: filePath,
          size: stats.size,
          encoding,
        },
        metadata: {
          fileSize: stats.size,
          created: stats.birthtime.toISOString(),
          modified: stats.mtime.toISOString(),
        },
      };
    } catch (error) {
      this.logger.error('Failed to write file', {
        error,
        path: params['path'],
      });
      return {
        success: false,
        error: error instanceof Error ? error.message : String(error),
      };
    }
  }

  /**
   * List directory contents
   */
  private async listDirectory(
    params: Record<string, any>
  ): Promise<ToolResult> {
    try {
      const dirPath = this.validateAndResolvePath(params['path']);
      const recursive = params['recursive'] || false;
      const showHidden = params['show_hidden'] || false;

      this.logger.debug('Listing directory', {
        path: dirPath,
        recursive,
        showHidden,
      });

      // Check if it's a directory
      const stats = await fs.stat(dirPath);
      if (!stats.isDirectory()) {
        throw new ToolError(
          `Path is not a directory: ${dirPath}`,
          'list_directory'
        );
      }

      const entries = await this.listDirectoryRecursive(
        dirPath,
        recursive,
        showHidden
      );

      return {
        success: true,
        result: {
          path: dirPath,
          entries,
          total: entries.length,
        },
        metadata: {
          recursive,
          showHidden,
          scannedAt: new Date().toISOString(),
        },
      };
    } catch (error) {
      this.logger.error('Failed to list directory', {
        error,
        path: params['path'],
      });
      return {
        success: false,
        error: error instanceof Error ? error.message : String(error),
      };
    }
  }

  /**
   * Create directory
   */
  private async createDirectory(
    params: Record<string, any>
  ): Promise<ToolResult> {
    try {
      const dirPath = this.validateAndResolvePath(params['path']);
      const recursive = params['recursive'] !== false; // default true

      this.logger.debug('Creating directory', { path: dirPath, recursive });

      await fs.mkdir(dirPath, { recursive });

      const stats = await fs.stat(dirPath);

      return {
        success: true,
        result: {
          path: dirPath,
          created: true,
        },
        metadata: {
          recursive,
          createdAt: stats.birthtime.toISOString(),
        },
      };
    } catch (error) {
      this.logger.error('Failed to create directory', {
        error,
        path: params['path'],
      });
      return {
        success: false,
        error: error instanceof Error ? error.message : String(error),
      };
    }
  }

  /**
   * Delete file or directory
   */
  private async deleteFile(params: Record<string, any>): Promise<ToolResult> {
    try {
      const targetPath = this.validateAndResolvePath(params['path']);
      const recursive = params['recursive'] || false;

      this.logger.debug('Deleting file/directory', {
        path: targetPath,
        recursive,
      });

      const stats = await fs.stat(targetPath);

      if (stats.isDirectory()) {
        if (recursive) {
          await fs.rm(targetPath, { recursive: true, force: true });
        } else {
          await fs.rmdir(targetPath);
        }
      } else {
        await fs.unlink(targetPath);
      }

      return {
        success: true,
        result: {
          path: targetPath,
          deleted: true,
          wasDirectory: stats.isDirectory(),
        },
        metadata: {
          recursive,
          deletedAt: new Date().toISOString(),
          originalSize: stats.size,
        },
      };
    } catch (error) {
      this.logger.error('Failed to delete file/directory', {
        error,
        path: params['path'],
      });
      return {
        success: false,
        error: error instanceof Error ? error.message : String(error),
      };
    }
  }

  /**
   * Get file information
   */
  private async getFileInfo(params: Record<string, any>): Promise<ToolResult> {
    try {
      const targetPath = this.validateAndResolvePath(params['path']);

      this.logger.debug('Getting file info', { path: targetPath });

      const stats = await fs.stat(targetPath);

      return {
        success: true,
        result: {
          path: targetPath,
          size: stats.size,
          isFile: stats.isFile(),
          isDirectory: stats.isDirectory(),
          isSymbolicLink: stats.isSymbolicLink(),
          permissions: stats.mode,
          created: stats.birthtime.toISOString(),
          modified: stats.mtime.toISOString(),
          accessed: stats.atime.toISOString(),
        },
        metadata: {
          dev: stats.dev,
          ino: stats.ino,
          nlink: stats.nlink,
          uid: stats.uid,
          gid: stats.gid,
        },
      };
    } catch (error) {
      this.logger.error('Failed to get file info', {
        error,
        path: params['path'],
      });
      return {
        success: false,
        error: error instanceof Error ? error.message : String(error),
      };
    }
  }

  /**
   * Validate and resolve file path
   */
  private validateAndResolvePath(inputPath: string): string {
    if (!inputPath || typeof inputPath !== 'string') {
      throw new ToolError(
        'Invalid path: must be a non-empty string',
        'filesystem'
      );
    }

    // Resolve the path to handle relative paths and symlinks
    const resolvedPath = path.resolve(inputPath);

    // Check if path is within allowed directories
    const isAllowed = this.fsConfig.allowedPaths.some((allowedPath) => {
      const normalizedAllowed = path.resolve(allowedPath);
      return resolvedPath.startsWith(normalizedAllowed);
    });

    if (!isAllowed) {
      throw new ToolError(
        `Path not allowed: ${resolvedPath}. Allowed paths: ${this.fsConfig.allowedPaths.join(', ')}`,
        'filesystem'
      );
    }

    // Check forbidden paths
    if (this.fsConfig.forbiddenPaths) {
      const isForbidden = this.fsConfig.forbiddenPaths.some((forbiddenPath) => {
        const normalizedForbidden = path.resolve(forbiddenPath);
        return resolvedPath.startsWith(normalizedForbidden);
      });

      if (isForbidden) {
        throw new ToolError(
          `Access to path forbidden: ${resolvedPath}`,
          'filesystem'
        );
      }
    }

    return resolvedPath;
  }

  /**
   * List directory contents recursively
   */
  private async listDirectoryRecursive(
    dirPath: string,
    recursive: boolean,
    showHidden: boolean
  ): Promise<any[]> {
    const entries: any[] = [];

    const dirents = await fs.readdir(dirPath, { withFileTypes: true });

    for (const dirent of dirents) {
      // Skip hidden files if not requested
      if (!showHidden && dirent.name.startsWith('.')) {
        continue;
      }

      const fullPath = path.join(dirPath, dirent.name);
      const relativePath = path.relative(process.cwd(), fullPath);

      try {
        const stats = await fs.stat(fullPath);

        const entry = {
          name: dirent.name,
          path: relativePath,
          fullPath,
          type: dirent.isDirectory() ? 'directory' : 'file',
          size: stats.size,
          modified: stats.mtime.toISOString(),
          permissions: stats.mode,
        };

        entries.push(entry);

        // Recurse into subdirectories if requested
        if (recursive && dirent.isDirectory()) {
          const subEntries = await this.listDirectoryRecursive(
            fullPath,
            true,
            showHidden
          );
          entries.push(...subEntries);
        }
      } catch (error) {
        // Log error but continue with other entries
        this.logger.warn('Failed to get stats for entry', {
          path: fullPath,
          error: error instanceof Error ? error.message : String(error),
        });
      }
    }

    return entries;
  }
}
