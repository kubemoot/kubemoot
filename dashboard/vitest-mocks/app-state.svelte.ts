/** Test stand-in for $app/state: a page whose URL a test sets and navigation changes. */
export const page = $state<{ url: URL; route: { id: string }; params: Record<string, string> }>({
	url: new URL('http://localhost/dashboard/'),
	route: { id: '/' },
	params: {}
});

export function setPageUrl(path: string): void {
	page.url = new URL(path, 'http://localhost');
}
