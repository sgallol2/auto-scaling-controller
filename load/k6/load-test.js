import http from 'k6/http';
import { check, sleep } from 'k6';

const target = __ENV.TARGET_URL;
const peakVUs = Number(__ENV.PEAK_VUS || 50); 

if (!target) {
  throw new Error('TARGET_URL is required, for example http://alb-dns-name');
}

export const options = {
  stages: [
    { duration: '30s', target: peakVUs },  // Rampa rápida para subir CPU (1 min)
    { duration: '3m', target: peakVUs },  // Carga constante sostenida (6 min -> asegura al menos 3 evaluaciones del controller)
    { duration: '30s', target: 0 },        // Detener tráfico para forzar el Scale In (1 min)
  ],
  thresholds: {
    http_req_failed: ['rate<0.05'],
    http_req_duration: ['p(95)<2000'],
  },
};

export default function () {
  const response = http.get(target);
  check(response, {
    'application responds': (res) => res.status >= 200 && res.status < 500,
  });
}
