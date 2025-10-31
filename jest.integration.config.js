module.exports = {
  preset: 'ts-jest',
  testEnvironment: 'node',
  testMatch: ['**/tests/integration/**/*.test.ts', '**/tests/integration/**/*.spec.ts'],
  testPathIgnorePatterns: ['/node_modules/', '/dist/', '/tests/unit/', '/tests/fixtures/'],

  moduleNameMapper: {
    '^@/(.*)$': '<rootDir>/src/$1',
    '^@tests/(.*)$': '<rootDir>/tests/$1',
  },

  setupFiles: ['<rootDir>/tests/integration/env.ts'],
  setupFilesAfterEnv: ['<rootDir>/tests/integration/setup.ts'],
  globalTeardown: '<rootDir>/tests/integration/teardown.ts',
  testSequencer: '<rootDir>/tests/integration/sequencer.js',

  transform: {
    '^.+\\.tsx?$': ['ts-jest', { tsconfig: 'tsconfig.json' }],
  },

  testTimeout: 70000,
  maxWorkers: 8,
  clearMocks: true,
  restoreMocks: true,
  errorOnDeprecated: true,
};
