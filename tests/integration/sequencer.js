/**
 * Integration test sequencer
 * 
 * Controls the order in which integration tests run.
 * Ensures tests that depend on each other run in the correct sequence.
 */

const Sequencer = require('@jest/test-sequencer').default;

class CustomSequencer extends Sequencer {
  sort(tests) {
    // Copy array to avoid mutation
    const testArray = Array.from(tests);
    
    // Run tests in a deterministic order
    // Integration tests should run sequentially (not in parallel)
    // to avoid conflicts with shared resources like cursor-agent CLI
    
    // Sort alphabetically by path for consistency
    return testArray.sort((testA, testB) => {
      if (testA.path < testB.path) return -1;
      if (testA.path > testB.path) return 1;
      return 0;
    });
  }
}

module.exports = CustomSequencer;
