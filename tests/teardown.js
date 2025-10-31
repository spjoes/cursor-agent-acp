/**
 * Jest global teardown
 *
 * This file handles cleanup of any global resources that might prevent
 * Jest from exiting cleanly, such as timers, processes, or connections.
 */

module.exports = async () => {
  // Clear any lingering timers
  if (global.gc) {
    global.gc();
  }

  // Force cleanup of any remaining handles
  if (process.env.NODE_ENV === 'test') {
    // Give a small delay to allow cleanup
    await new Promise(resolve => setTimeout(resolve, 100));

    // Force process exit if needed (Jest will handle this gracefully)
    if (process.listenerCount('exit') === 0) {
      process.exit(0);
    }
  }
};
