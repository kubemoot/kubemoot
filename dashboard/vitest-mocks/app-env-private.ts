// Test stand-in for $app/env/private: the variables declared in src/env.ts, each a live
// binding that tests change with setPrivateEnv and clear with resetPrivateEnv.
export let OPERATOR_REPORT_URL: string | undefined;

export function setPrivateEnv(values: { OPERATOR_REPORT_URL?: string }): void {
	OPERATOR_REPORT_URL = values.OPERATOR_REPORT_URL;
}

export function resetPrivateEnv(): void {
	OPERATOR_REPORT_URL = undefined;
}
