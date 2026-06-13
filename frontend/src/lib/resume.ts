// Static résumé-derived content (see CONTEXT.md "Static Content"). Lives in the
// frontend, not the database — it changes rarely and needs no API.

export const profile = {
  name: 'Ahmad Alifyandra',
  nickname: 'Alif',
  title: 'Full-Stack Engineer',
  location: 'Melbourne, VIC',
  email: 'alifyandra@gmail.com',
  github: 'https://github.com/alifyandra',
  linkedin: 'https://linkedin.com/in/alifyandra',
  spotify: 'https://open.spotify.com/user/alifyandraid',
  summary:
    "Software engineer based in Melbourne, currently working with Python, Django and TypeScript to build systems that deal with complex data and real production workloads.\n\nI've worked across backend, full-stack and infra, from payment systems in fintech to an AI analytics platform I helped rebuild to be more scalable with Django, Docker, Redis, Celery and Next.js, cutting processing time by around 80%.",
};

export const skills: { group: string; items: string[] }[] = [
  { group: 'Languages', items: ['Python', 'Go', 'Java', 'TypeScript', 'C'] },
  {
    group: 'Backend',
    items: ['Django / DRF', 'Spring', 'Spark', 'Node.js', 'REST', 'Webhooks'],
  },
  { group: 'Frontend', items: ['React', 'Next.js', 'React Native', 'Flutter'] },
  { group: 'Data', items: ['PostgreSQL', 'Redis', 'Firestore'] },
  { group: 'Async & Messaging', items: ['Celery', 'RabbitMQ', 'SQS'] },
  {
    group: 'Cloud & DevOps',
    items: ['Docker', 'GitHub Actions', 'AWS', 'DigitalOcean', 'GCP', 'Ansible'],
  },
  { group: 'Security', items: ['JWT', '2FA', 'Secure API Design'] },
];

export type Experience = {
  role: string;
  company: string;
  period: string;
  points: string[];
};

export const experience: Experience[] = [
  {
    role: 'Software Engineer',
    company: 'FOUNDIT',
    period: 'May 2026 – Present',
    points: [
      'Building full-stack at FOUNDIT, an early-stage proptech startup, leading one of their products.',
    ],
  },
  {
    role: 'Software Engineer',
    company: 'Openresim Pty Ltd',
    period: 'Jul 2024 – May 2026',
    points: [
      'Led the redevelopment of a legacy analytics system into a scalable, AI-driven SaaS platform using Django REST Framework and Next.js.',
      'Reduced analysis computation time by ~80% via query redesign and indexing.',
      'Built Redis caching and Celery task queues for long-running calculations, with real-time progress over websockets.',
      'Introduced Docker and GitHub Actions CI/CD, deploying to DigitalOcean with managed Postgres and object storage.',
    ],
  },
  {
    role: 'Software Engineer',
    company: 'OY! Indonesia (Fintech)',
    period: 'Sep 2021 – May 2022',
    points: [
      'Built payment acceptance systems serving hundreds of thousands of users and enterprise merchants.',
      'Implemented OVO e-wallet integration and webhook-driven transaction services with Java Spring & Spark.',
      'Used RabbitMQ for asynchronous financial transaction processing.',
    ],
  },
  {
    role: 'Software Engineer Intern',
    company: 'TKA Developments',
    period: 'Mar 2021 – Aug 2021',
    points: [
      'Built three campaign-based mobile/web apps (community, wealth-tracking, social e-commerce).',
      'Designed NoSQL schemas and integrated the MidTrans payment gateway.',
      'Deployed via AWS S3 and Elastic Beanstalk.',
    ],
  },
];

export const education = [
  {
    title: 'Bachelor of Computer Science (Cyber Security Major)',
    org: 'University of Queensland',
    year: '2023',
  },
  {
    title: 'Bachelor of Computer Science',
    org: 'University of Indonesia',
    year: '2023',
  },
  {
    title: 'AWS Certified Solutions Architect – Associate',
    org: 'In progress',
    year: 'expected May 2026',
  },
];
