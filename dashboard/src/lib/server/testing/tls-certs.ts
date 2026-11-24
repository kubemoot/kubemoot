// Test helper: a CA and a localhost serving certificate it signs, made with openssl.
import { execFileSync } from 'node:child_process';
import { writeFileSync } from 'node:fs';
import { join } from 'node:path';

/** Makes a CA and a localhost serving certificate it signs, with openssl, in a temp directory. */
export function makeCerts(dir: string, name: string) {
	const sh = (...args: string[]) => execFileSync('/usr/bin/openssl', args, { cwd: dir, stdio: 'pipe' });
	sh(
		'req',
		'-x509',
		'-newkey',
		'ec',
		'-pkeyopt',
		'ec_paramgen_curve:prime256v1',
		'-nodes',
		'-keyout',
		`${name}-ca.key`,
		'-out',
		`${name}-ca.crt`,
		'-days',
		'2',
		'-subj',
		`/CN=${name}-ca`,
		'-addext',
		'basicConstraints=critical,CA:TRUE',
		'-addext',
		'keyUsage=critical,keyCertSign'
	);
	sh(
		'req',
		'-newkey',
		'ec',
		'-pkeyopt',
		'ec_paramgen_curve:prime256v1',
		'-nodes',
		'-keyout',
		`${name}.key`,
		'-out',
		`${name}.csr`,
		'-subj',
		'/CN=localhost'
	);
	writeFileSync(join(dir, `${name}.ext`), 'subjectAltName=DNS:localhost,IP:127.0.0.1\nextendedKeyUsage=serverAuth\n');
	sh(
		'x509',
		'-req',
		'-in',
		`${name}.csr`,
		'-CA',
		`${name}-ca.crt`,
		'-CAkey',
		`${name}-ca.key`,
		'-CAcreateserial',
		'-out',
		`${name}.crt`,
		'-days',
		'2',
		'-extfile',
		`${name}.ext`
	);
}

/** Signs the CSR makeCerts left for `name` with its CA, for a period that ended in the past; returns the file. */
export function makeExpiredLeaf(dir: string, name: string): string {
	const sh = (...args: string[]) => execFileSync('/usr/bin/openssl', args, { cwd: dir, stdio: 'pipe' });
	writeFileSync(
		join(dir, 'ca.cnf'),
		'[ca]\ndefault_ca=d\n[d]\ndatabase=index.txt\nnew_certs_dir=.\nserial=serial.txt\ndefault_md=sha256\npolicy=p\nunique_subject=no\n[p]\ncommonName=supplied\n'
	);
	writeFileSync(join(dir, 'index.txt'), '');
	writeFileSync(join(dir, 'serial.txt'), '0A\n');
	const out = `${name}-expired.crt`;
	sh(
		'ca',
		'-batch',
		'-config',
		'ca.cnf',
		'-cert',
		`${name}-ca.crt`,
		'-keyfile',
		`${name}-ca.key`,
		'-in',
		`${name}.csr`,
		'-out',
		out,
		'-startdate',
		'20200101000000Z',
		'-enddate',
		'20200102000000Z',
		'-extfile',
		`${name}.ext`,
		'-notext'
	);
	return join(dir, out);
}
