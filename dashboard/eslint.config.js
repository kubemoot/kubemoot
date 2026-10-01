import js from '@eslint/js';
import ts from 'typescript-eslint';
import svelte from 'eslint-plugin-svelte';
import sonarjs from 'eslint-plugin-sonarjs';
import unicorn from 'eslint-plugin-unicorn';
import prettier from 'eslint-config-prettier';
import globals from 'globals';

// SonarQube is the authority on quality; the block for src/ is its fast local check.
// It runs the rules of the server's "Sonar way" TypeScript profile that eslint can run:
// the sonarjs recommended set, less the rules the profile leaves inactive, plus the
// typescript-eslint and unicorn rules Sonar runs under its own keys. Rules that report
// a Sonar security hotspot (super-linear-regex S5852, pseudo-random S2245,
// no-clear-text-protocols S5332) stay on, so each site carries a reviewed reason.
//
// notInSonarWay: the rules of the sonarjs 4.2 recommended set whose Sonar keys are not
// active in the server's profile, from api/rules/search?qprofile=<Sonar way ts>&activation=true.
// Compare again when the plugin or the profile changes.
const notInSonarWay = [
	'no-extra-arguments', 'prefer-single-boolean-return', 'no-floating-point-equality',
	'no-unused-vars', 'future-reserved-words', 'null-dereference', 'no-implicit-global',
	'no-fixed-wait-in-tests', 'different-types-comparison', 'updated-const-var',
	'inconsistent-function-call', 'argument-type', 'in-operator-type-error',
	'array-callback-without-return', 'function-return-type',
	'no-incompatible-assertion-types', 'prefer-specific-assertions', 'no-trivial-assertions',
	'parameterized-tests', 'no-duplicate-test-title', 'async-test-assertions',
	'no-empty-test-title', 'hooks-before-test-cases', 'no-forced-browser-interaction',
	'assertions-in-test-cases', 'synchronous-suite-callback',
	'prefer-native-lodash-alternative', 'no-default-utility-imports', 'memoize-cache-key',
	'no-debug-commands-in-ui-tests', 'no-interpolation-in-inline-snapshots',
	'explicit-test-skip', 'no-empty-parameterized-test-dataset',
	'testing-library-query-assertion', 'synchronous-exception-assertions',
	'no-duplicate-parameterized-test-case', 'no-debounce-throttle-in-render',
	'avoid-mutating-nested-properties-of-shallow-clones', 'prefer-native-jquery-alternative',
	'no-vue-class-component', 'no-vue-mixins',
	'testing-library-prefer-query-by-disappearance', 'prefer-cypress-should',
	'no-mutate-reactive-state-in-updated-hook', 'vitest-mock-at-module-scope',
	'prefer-native-axios-alternative'
];

export default [
	{ ignores: ['.svelte-kit/', 'build/', 'node_modules/', 'coverage/'] },

	js.configs.recommended,
	...ts.configs.recommended,
	...svelte.configs['flat/recommended'],

	// Prettier compatibility - disables rules that conflict with prettier formatting
	prettier,
	...svelte.configs['flat/prettier'],

	{
		languageOptions: {
			globals: {
				...globals.browser,
				...globals.node
			}
		}
	},

	// TypeScript parser inside .svelte script blocks
	{
		files: ['**/*.svelte'],
		languageOptions: {
			parserOptions: {
				parser: ts.parser,
				extraFileExtensions: ['.svelte']
			}
		}
	},

	// .svelte.ts / .svelte.js rune modules are plain TS/JS modules; the svelte
	// flat config globs them but its parser cannot handle `interface`/type syntax,
	// so apply the TS parser directly to them.
	{
		files: ['**/*.svelte.ts', '**/*.svelte.js'],
		languageOptions: {
			parser: ts.parser
		}
	},

	{
		rules: {
			complexity: ['error', 10],
			'@typescript-eslint/no-unused-vars': ['error', { argsIgnorePattern: '^_' }],

			// Not Sonar rules; svelte plugin debt kept off and tracked in the backlog.
			// paths.base = '/dashboard' means every href/goto needs resolve().
			'svelte/no-navigation-without-resolve': 'off',
			'svelte/require-each-key': 'off',
			'svelte/no-useless-mustaches': 'off',
			// {@html} renders coordinator-generated markdown through marked.
			'svelte/no-at-html-tags': 'off',
			'svelte/prefer-svelte-reactivity': 'off',
			'svelte/no-useless-children-snippet': 'off',
			'@typescript-eslint/no-explicit-any': 'off',
			// Fires on Svelte reactive/template expression statements.
			'@typescript-eslint/no-unused-expressions': 'off'
		}
	},

	{
		name: 'dashboard/sonar-way-src',
		files: ['src/**/*.ts', 'src/**/*.svelte'],
		languageOptions: {
			parserOptions: { projectService: true, tsconfigRootDir: import.meta.dirname }
		},
		plugins: { ...sonarjs.configs.recommended.plugins, unicorn },
		rules: {
			...sonarjs.configs.recommended.rules,
			...Object.fromEntries(notInSonarWay.map((rule) => [`sonarjs/${rule}`, 'off'])),
			'sonarjs/no-commented-code': 'error', // S125, in the profile but not in recommended
			'@typescript-eslint/no-base-to-string': 'error', // S6551
			'@typescript-eslint/prefer-readonly': 'error', // S2933
			'@typescript-eslint/prefer-string-starts-ends-with': 'error', // S6557
			'@typescript-eslint/no-unnecessary-type-assertion': 'error', // S4325
			'@typescript-eslint/no-misused-promises': 'error', // S6544
			'no-duplicate-imports': ['error', { allowSeparateTypeImports: true }], // S3863
			'unicorn/prefer-single-call': 'error', // S7778
			'unicorn/prefer-number-properties': 'error', // S7773
			'unicorn/prefer-string-raw': 'error', // S7780
			'unicorn/prefer-string-replace-all': 'error', // S7781
			'unicorn/prefer-export-from': 'error', // S7763
			'unicorn/prefer-array-find': 'error' // S7750
		}
	},

	// Two sonarjs rules cannot read .svelte files correctly:
	// - sonarjs/deprecation reads positions from the TS program the svelte parser
	//   builds from generated code, and crashes. Deprecated API use in .svelte files
	//   (e.g. `base` from $app/paths, still imported by the pages) is therefore not
	//   linted; moving those pages to resolve() is tracked backlog.
	// - sonarjs/no-use-of-empty-return-value reads {@render snippet()} as using the
	//   result of a function that returns nothing; rendering a snippet is the Svelte 5
	//   idiom and has no return value to use.
	{
		files: ['src/**/*.svelte'],
		rules: {
			'sonarjs/deprecation': 'off',
			'sonarjs/no-use-of-empty-return-value': 'off'
		}
	}
];
