/** Test stand-in for $app/state: a page whose URL and data a test sets and navigation changes. */
export const page = $state<{ url: URL; data: Record<string, unknown>; route: { id: string }; params: Record<string, string> }>({
	url: new URL('http://localhost/dashboard/'),
	data: {},
	route: { id: '/' },
	params: {}
});

export function setPageUrl(path: string): void {
	page.url = new URL(path, 'http://localhost');
}

/** Sets what the root layout would load for the page, such as the signed-in user. */
export function setPageData(data: Record<string, unknown>): void {
	page.data = data;
}
